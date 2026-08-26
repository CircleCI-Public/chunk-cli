// Package ciconfig extracts candidate validation commands from a repository's
// checked-in CircleCI configuration.
//
// It is a reducer, not an interpreter. A real config can run to thousands of
// lines; this narrows it to the handful of `run` steps belonging to jobs that
// gate the default branch, so a caller can classify that short list into
// test/lint/format roles instead of guessing from root filenames.
//
// Constructs it cannot resolve are reported rather than guessed at: orb steps
// hide their contents behind a name, and a `setup: true` config generates the
// real one at run time. In both cases an empty candidate list means "could not
// tell", not "nothing to run" — callers must check Dynamic and SkippedOrbs
// before treating a miss as a clean negative.
package ciconfig

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNotFound reports that workDir has no CircleCI config.
var ErrNotFound = errors.New("no CircleCI config found")

const (
	// maxCandidates bounds the returned list so a pathological config cannot
	// blow up a downstream prompt. Overflow is counted in Result.Truncated.
	maxCandidates = 200

	// maxDepth bounds custom-command expansion. Commands may invoke commands;
	// a cycle would otherwise recurse forever.
	maxDepth = 4
)

// defaultBranches are the branch names a job must run on to count as a gate.
var defaultBranches = []string{"main", "master"}

// Candidate is one `run` step from a job that gates the default branch.
type Candidate struct {
	Job     string // job definition name
	Step    string // step name, empty if the step was unnamed
	Command string // shell command, with parameters substituted

	// WorkingDir is the step's own working_directory, empty when unset. Only
	// a step-level directory narrows where a command runs: a job-level
	// working_directory is where checkout places the repo, so it is the
	// repo root and every step in the job already runs relative to it.
	WorkingDir string
}

// Result is what Extract could and could not determine from a config.
type Result struct {
	Path       string      // config file the result came from
	Candidates []Candidate // run steps from default-branch jobs, in config order

	// Dynamic reports a `setup: true` config. The checked-in file only
	// generates the real config at run time, so Candidates is not meaningful.
	Dynamic bool

	// SkippedOrbs lists orb-provided steps whose commands are not in this
	// file, e.g. "node/install-packages".
	SkippedOrbs []string

	// Truncated counts candidates dropped at maxCandidates.
	Truncated int

	// Unresolved counts run steps skipped because they still referenced a
	// parameter that could not be substituted.
	Unresolved int
}

// Extract reads workDir's CircleCI config and returns the run steps belonging
// to jobs that gate the default branch. It returns ErrNotFound if no config
// exists, so callers can fall back to filename-based detection.
func Extract(workDir string) (*Result, error) {
	path, data, err := read(workDir)
	if err != nil {
		return nil, err
	}

	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	res := &Result{Path: path, Dynamic: f.Setup}
	if f.Setup {
		return res, nil
	}

	e := &extractor{commands: f.Commands, orbs: f.Orbs, res: res, seen: map[string]bool{}}
	for _, wj := range gateJobs(f) {
		job, ok := f.Jobs[wj.Name]
		if !ok {
			// Job comes from an orb, not this file.
			e.noteOrb(wj.Name)
			continue
		}
		jc := jobCtx{name: wj.Name}
		args := mergeArgs(job.Parameters, wj.Params)

		// pre-steps and post-steps are injected around the job's own steps by
		// the workflow that invokes it.
		e.walk(jc, wj.PreSteps, args, 0)
		e.walk(jc, job.Steps, args, 0)
		e.walk(jc, wj.PostSteps, args, 0)
	}

	sort.Strings(res.SkippedOrbs)
	return res, nil
}

// read locates and reads the config, preferring the .yml spelling.
func read(workDir string) (string, []byte, error) {
	for _, name := range []string{"config.yml", "config.yaml"} {
		path := filepath.Join(workDir, ".circleci", name)
		data, err := os.ReadFile(path)
		if err == nil {
			return path, data, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("read %s: %w", path, err)
		}
	}
	return "", nil, ErrNotFound
}

// gateJobs returns the workflow entries that run on the default branch, in
// config order, deduplicated. Approval holds, branch-filtered jobs and
// scheduled workflows are excluded.
//
// Workflows are visited in the order they appear in the file rather than
// alphabetically: the caller keeps the first command it finds per role, and a
// config's primary workflow is conventionally written first. Sorting by name
// would let an unrelated workflow that happens to sort earlier supply the
// commands.
func gateJobs(f file) []workflowJob {
	if f.Workflows.Kind != yaml.MappingNode || len(f.Workflows.Content) == 0 {
		// A CircleCI 2.0 config with no workflows block runs the job named
		// "build" implicitly.
		if _, ok := f.Jobs["build"]; ok {
			return []workflowJob{{Name: "build"}}
		}
		return nil
	}

	var out []workflowJob
	seen := map[string]bool{}
	for i := 0; i+1 < len(f.Workflows.Content); i += 2 {
		node := f.Workflows.Content[i+1]
		if node.Kind != yaml.MappingNode {
			// `workflows: version: 2` — a scalar, not a workflow.
			continue
		}
		var w workflow
		if err := node.Decode(&w); err != nil {
			continue
		}
		if len(w.Triggers) > 0 {
			// A workflow with `triggers:` runs on a schedule, not on a push, so
			// its jobs do not gate the branch. Nightlies are also routinely a
			// slower superset of the real gates — exactly what a developer
			// should not be handed as an inner-loop command.
			continue
		}
		for _, j := range w.Jobs {
			if j.Name == "" || j.Type == "approval" {
				continue
			}
			if !runsOnDefaultBranch(j.Filters) {
				continue
			}
			// Key on the parameters too: the same job invoked with different
			// values produces different commands.
			key := j.Name + "\x00" + fmt.Sprint(j.Params)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, j)
		}
	}
	return out
}

// runsOnDefaultBranch reports whether a job's filters let it run on main.
func runsOnDefaultBranch(f filters) bool {
	if slices.ContainsFunc(f.Branches.Ignore, matchesDefaultBranch) {
		return false
	}
	if len(f.Branches.Only) == 0 {
		return true
	}
	return slices.ContainsFunc(f.Branches.Only, matchesDefaultBranch)
}

// matchesDefaultBranch reports whether a CircleCI branch pattern — a literal
// name or a /regex/ — matches a default branch name. An uncompilable regex
// matches, so an unparseable filter keeps the job rather than dropping it.
func matchesDefaultBranch(pattern string) bool {
	if len(pattern) > 1 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		re, err := regexp.Compile(strings.Trim(pattern, "/"))
		if err != nil {
			return true
		}
		return slices.ContainsFunc(defaultBranches, re.MatchString)
	}
	return slices.Contains(defaultBranches, pattern)
}

// extractor accumulates candidates while walking a job's step tree.
type extractor struct {
	commands map[string]command
	orbs     map[string]string
	res      *Result
	seen     map[string]bool // deduplicates identical commands
}

// jobCtx carries the job-level facts a candidate needs to be interpretable.
type jobCtx struct {
	name string
}

// walk collects run steps from steps, substituting args into any parameter
// references. depth bounds custom-command expansion.
func (e *extractor) walk(jc jobCtx, steps []step, args map[string]string, depth int) {
	for _, s := range steps {
		switch {
		case s.Kind == "run":
			e.addRun(jc, s, args)

		case s.Kind == "when" || s.Kind == "unless":
			// Conditional steps still gate the branch when the condition holds.
			e.walk(jc, s.Nested, args, depth)

		case e.isCustomCommand(s.Kind):
			if depth >= maxDepth {
				continue
			}
			cmd := e.commands[s.Kind]
			e.walk(jc, cmd.Steps, mergeArgs(cmd.Parameters, substituteAll(s.Params, args)), depth+1)
			// A command taking a `steps:` parameter inlines those steps into
			// its body, so they run in this job too.
			e.walk(jc, s.Nested, args, depth)

		case strings.Contains(s.Kind, "/"):
			e.noteOrb(s.Kind)
			// Orb commands routinely wrap the caller's own steps in a
			// `steps:` parameter — go/with-cache and friends. The orb's own
			// steps stay opaque, but the ones handed to it are ours to read.
			e.walk(jc, s.Nested, args, depth)
		}
		// Anything else is a built-in with no command of its own —
		// checkout, save_cache, store_test_results, and friends.
	}
}

// addRun records a run step as a candidate, unless it is a background process,
// a duplicate, or still carries an unsubstituted parameter.
func (e *extractor) addRun(jc jobCtx, s step, args map[string]string) {
	if s.Background {
		return
	}
	cmd := strings.TrimSpace(substitute(s.Command, args))
	if cmd == "" {
		return
	}
	if paramRef.MatchString(cmd) {
		e.res.Unresolved++
		return
	}
	// Identical commands in different working directories are different work.
	key := s.WorkingDir + "\x00" + cmd
	if e.seen[key] {
		return
	}
	if len(e.res.Candidates) >= maxCandidates {
		e.res.Truncated++
		return
	}
	e.seen[key] = true
	e.res.Candidates = append(e.res.Candidates, Candidate{
		Job:        jc.name,
		Step:       s.Name,
		Command:    cmd,
		WorkingDir: s.WorkingDir,
	})
}

func (e *extractor) isCustomCommand(kind string) bool {
	_, ok := e.commands[kind]
	return ok
}

func (e *extractor) noteOrb(name string) {
	if slices.Contains(e.res.SkippedOrbs, name) {
		return
	}
	e.res.SkippedOrbs = append(e.res.SkippedOrbs, name)
}

// paramRef matches a CircleCI parameter interpolation, e.g. << parameters.x >>.
var paramRef = regexp.MustCompile(`<<\s*parameters\.([A-Za-z0-9_-]+)\s*>>`)

// substitute replaces << parameters.name >> references with args values,
// leaving unknown references in place for the caller to detect.
func substitute(s string, args map[string]string) string {
	if len(args) == 0 || !strings.Contains(s, "<<") {
		return s
	}
	return paramRef.ReplaceAllStringFunc(s, func(match string) string {
		name := paramRef.FindStringSubmatch(match)[1]
		if v, ok := args[name]; ok {
			return v
		}
		return match
	})
}

// substituteAll resolves parameter references in the values a step passes to a
// custom command. A job routinely forwards its own parameter through —
// `run-suite: {suite: << parameters.suite >>}` — and without this the value
// reaches the command body unresolved and the step is discarded.
func substituteAll(params, args map[string]string) map[string]string {
	if len(params) == 0 || len(args) == 0 {
		return params
	}
	out := make(map[string]string, len(params))
	for name, v := range params {
		out[name] = substitute(v, args)
	}
	return out
}

// file is the subset of a CircleCI config this package reads.
type file struct {
	Setup    bool               `yaml:"setup"`
	Orbs     map[string]string  `yaml:"orbs"`
	Commands map[string]command `yaml:"commands"`
	Jobs     map[string]job     `yaml:"jobs"`

	// Workflows stays a node so it can be walked in document order; a map
	// would lose the ordering the caller relies on to pick a primary workflow.
	Workflows yaml.Node `yaml:"workflows"`
}

type job struct {
	Steps      []step               `yaml:"steps"`
	Parameters map[string]parameter `yaml:"parameters"`
}

type command struct {
	Steps      []step               `yaml:"steps"`
	Parameters map[string]parameter `yaml:"parameters"`
}

// mergeArgs combines declared parameter defaults with the values the caller
// passed, with the passed values winning.
func mergeArgs(declared map[string]parameter, passed map[string]string) map[string]string {
	if len(declared) == 0 && len(passed) == 0 {
		return nil
	}
	out := make(map[string]string, len(declared)+len(passed))
	for name, p := range declared {
		if p.Default.Kind == yaml.ScalarNode {
			out[name] = p.Default.Value
		}
	}
	maps.Copy(out, passed)
	return out
}

type parameter struct {
	Default yaml.Node `yaml:"default"`
}

type workflow struct {
	Jobs []workflowJob `yaml:"jobs"`

	// Triggers is only inspected for presence: any trigger block means the
	// workflow is scheduled rather than run on a push.
	Triggers []yaml.Node `yaml:"triggers"`
}

// workflowJob is one entry in a workflow's job list, which YAML allows to be
// either a bare name or a single-key map of name to overrides.
type workflowJob struct {
	Name      string
	Type      string
	Filters   filters
	Params    map[string]string // job parameter values passed by the workflow
	PreSteps  []step
	PostSteps []step
}

// workflowJobKeys are the override keys CircleCI defines itself. Anything else
// in the block is a value for a parameter the job declared.
var workflowJobKeys = map[string]bool{
	"type": true, "filters": true, "requires": true, "context": true,
	"name": true, "matrix": true, "pre-steps": true, "post-steps": true,
	"serial-group": true,
}

func (w *workflowJob) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		w.Name = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode || len(n.Content) < 2 {
		return nil
	}
	w.Name = n.Content[0].Value

	var opts struct {
		Type      string  `yaml:"type"`
		Filters   filters `yaml:"filters"`
		PreSteps  []step  `yaml:"pre-steps"`
		PostSteps []step  `yaml:"post-steps"`
	}
	// A malformed override block should not drop the job.
	if err := n.Content[1].Decode(&opts); err != nil {
		return nil
	}
	w.Type, w.Filters = opts.Type, opts.Filters
	w.PreSteps, w.PostSteps = opts.PreSteps, opts.PostSteps

	for name, v := range scalarParams(n.Content[1]) {
		if workflowJobKeys[name] {
			continue
		}
		if w.Params == nil {
			w.Params = map[string]string{}
		}
		w.Params[name] = v
	}
	return nil
}

type filters struct {
	Branches branchFilter `yaml:"branches"`
}

type branchFilter struct {
	Only   stringList `yaml:"only"`
	Ignore stringList `yaml:"ignore"`
}

// stringList accepts either a scalar or a sequence, as CircleCI filters allow.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = stringList{n.Value}
		return nil
	}
	var out []string
	if err := n.Decode(&out); err != nil {
		return nil
	}
	*s = out
	return nil
}

// step is one entry in a job's step list: a bare built-in name, a run step, a
// conditional wrapping more steps, or an invocation of a custom or orb command.
type step struct {
	Kind       string            // "run", "checkout", "when", "node/install-packages", ...
	Name       string            // run step name
	Command    string            // run step command
	WorkingDir string            // run step's own working_directory, if narrowed
	Background bool              // background steps are processes, not gates
	Params     map[string]string // scalar params passed to a custom command
	Nested     []step            // steps under a when/unless
}

func (s *step) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s.Kind = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode || len(n.Content) < 2 {
		return nil
	}
	s.Kind = n.Content[0].Value
	value := n.Content[1]

	switch s.Kind {
	case "run":
		s.decodeRun(value)
	case "when", "unless":
		var w struct {
			Steps []step `yaml:"steps"`
		}
		if err := value.Decode(&w); err == nil {
			s.Nested = w.Steps
		}
	default:
		s.Params = scalarParams(value)
		s.Nested = nestedSteps(value)
	}
	return nil
}

// nestedSteps decodes a `steps:` parameter passed to a custom or orb command.
func nestedSteps(n *yaml.Node) []step {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "steps" || n.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		var steps []step
		if err := n.Content[i+1].Decode(&steps); err != nil {
			return nil
		}
		return steps
	}
	return nil
}

// decodeRun handles both `run: cmd` and the expanded mapping form.
func (s *step) decodeRun(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		s.Command = n.Value
		return
	}
	var r struct {
		Name       string `yaml:"name"`
		Command    string `yaml:"command"`
		Background bool   `yaml:"background"`
		WorkingDir string `yaml:"working_directory"`
	}
	if err := n.Decode(&r); err != nil {
		return
	}
	s.Name, s.Command, s.Background, s.WorkingDir = r.Name, r.Command, r.Background, r.WorkingDir
}

// scalarParams collects the scalar-valued keys of a mapping. Non-scalar
// parameters cannot be substituted into a command string, so they are dropped
// and the referencing step is reported as unresolved.
func scalarParams(n *yaml.Node) map[string]string {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	out := make(map[string]string, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i+1].Kind == yaml.ScalarNode {
			out[n.Content[i].Value] = n.Content[i+1].Value
		}
	}
	return out
}
