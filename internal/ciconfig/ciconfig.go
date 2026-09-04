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

// ConfigError reports a config that exists but could not be used — unreadable
// or unparseable. It carries Path so a caller can tell it apart from
// ErrNotFound: there is nothing to explain when a repo has no config, but a
// config that was found and rejected has to be said out loud rather than
// silently falling back to guessing from filenames.
type ConfigError struct {
	Op   string // "read" or "parse"
	Path string
	Err  error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err)
}

func (e *ConfigError) Unwrap() error { return e.Err }

const (
	// maxCandidates bounds the returned list so a pathological config cannot
	// blow up a downstream prompt. Overflow is counted in Result.Truncated.
	maxCandidates = 200

	// maxDepth bounds custom-command expansion. Commands may invoke commands;
	// a cycle would otherwise recurse forever.
	maxDepth = 4
)

// fallbackBranches are the branch names a job must run on to count as a gate
// when the caller does not name one. Both are guesses: a repo that defaults to
// develop has neither, and every job in its config would then look
// branch-filtered away. Options.DefaultBranch exists to avoid guessing.
var fallbackBranches = []string{"main", "master"}

// Options configures Extract.
type Options struct {
	// DefaultBranch is the branch whose checks count as gates — the branch a
	// PR merges into. Empty falls back to main and master; whichever names were
	// used come back in Result.Branches.
	DefaultBranch string
}

// Step kinds this package treats specially. Everything else is either a
// built-in with no command of its own or an invocation of a custom or orb
// command.
const (
	kindRun    = "run"
	kindWhen   = "when"
	kindUnless = "unless"
)

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

	// Unresolved counts run steps skipped because they still carried an
	// interpolation we could not substitute — an unbound parameter, or a
	// pipeline-time reference such as << pipeline.parameters.x >>.
	Unresolved int

	// Branches are the branch names gate selection matched filters against, so
	// a caller can name the branch its candidates gate.
	Branches []string

	// GateJobs counts the workflow entries that qualified as gates. Zero from a
	// non-dynamic config means no job in it runs on Branches — a different miss
	// from "the jobs ran but nothing in them classified", and the one a wrong
	// default branch produces.
	GateJobs int
}

// Extract reads workDir's CircleCI config and returns the run steps belonging
// to jobs that gate the default branch. It returns ErrNotFound if no config
// exists, so callers can fall back to filename-based detection, and a
// *ConfigError if one exists but cannot be read or parsed.
func Extract(workDir string, opts Options) (*Result, error) {
	path, data, err := read(workDir)
	if err != nil {
		return nil, err
	}

	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, &ConfigError{Op: "parse", Path: path, Err: err}
	}

	branches := fallbackBranches
	if opts.DefaultBranch != "" {
		branches = []string{opts.DefaultBranch}
	}

	res := &Result{Path: path, Dynamic: f.Setup, Branches: branches}
	if f.Setup {
		return res, nil
	}

	gates := gateJobs(f, branches)
	res.GateJobs = len(gates)

	e := &extractor{commands: f.Commands, orbs: f.Orbs, res: res, seen: map[string]bool{}}
	for _, wj := range gates {
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
			return "", nil, &ConfigError{Op: "read", Path: path, Err: err}
		}
	}
	return "", nil, ErrNotFound
}

// gateJobs returns the workflow entries that run on the default branch, in
// config order, deduplicated. Approval holds, branch-filtered jobs, scheduled
// workflows and workflows switched off by their own when:/unless: are
// excluded.
//
// Workflows are visited in the order they appear in the file rather than
// alphabetically: the caller keeps the first command it finds per role, and a
// config's primary workflow is conventionally written first. Sorting by name
// would let an unrelated workflow that happens to sort earlier supply the
// commands.
func gateJobs(f file, branches []string) []workflowJob {
	if f.Workflows.Kind != yaml.MappingNode || len(f.Workflows.Content) == 0 {
		// A CircleCI 2.0 config with no workflows block runs the job named
		// "build" implicitly.
		if _, ok := f.Jobs["build"]; ok {
			return []workflowJob{{Name: "build"}}
		}
		return nil
	}

	// A workflow condition resolves against the pipeline parameters the config
	// declares. Their defaults are all a checked-in file can offer: a value
	// supplied at trigger time is not in it.
	params := mergeArgs(f.Parameters, nil)

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
		if !workflowRuns(w, params) {
			// The workflow is switched off by default, so nothing in it gates a
			// push either.
			continue
		}
		for _, j := range w.Jobs {
			if j.Name == "" || j.Type == "approval" {
				continue
			}
			if !runsOnDefaultBranch(j.Filters, branches) {
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

// runsOnDefaultBranch reports whether a job's filters let it run on one of
// branches. A pattern we cannot read must not decide the job's fate, so each
// direction passes the fail-open answer for its own sense: an unreadable ignore
// counts as not matching, an unreadable only as matching. Both keep the job.
func runsOnDefaultBranch(f filters, branches []string) bool {
	if slices.ContainsFunc(f.Branches.Ignore, func(p string) bool {
		return matchesDefaultBranch(p, branches, false)
	}) {
		return false
	}
	if len(f.Branches.Only) == 0 {
		return true
	}
	return slices.ContainsFunc(f.Branches.Only, func(p string) bool {
		return matchesDefaultBranch(p, branches, true)
	})
}

// matchesDefaultBranch reports whether a CircleCI branch pattern — a literal
// name or a /regex/ — matches one of branches. onBadPattern is the answer for a
// regex that will not compile; it is the caller's because the value that keeps
// the job differs between only: and ignore:.
func matchesDefaultBranch(pattern string, branches []string, onBadPattern bool) bool {
	if len(pattern) > 1 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		// Only the delimiters come off. strings.Trim strips every leading and
		// trailing slash, so `/^release//` — a config with a stray slash, or a
		// pattern ending in an escaped one — arrived as `^release/` shorn to
		// `^release`, and `/^(feature|hotfix)\//` lost the `\` boundary it
		// needed and failed to compile: a release-only job then failed open
		// into a default-branch gate.
		body := strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
		// CircleCI matches a filter regex against the whole branch name — the
		// reason its own docs write `only: /^config-test.*/` with a trailing
		// `.*`. Matching unanchored reads `only: /^ma/`, which CircleCI runs on
		// no branch whatsoever, as a main-and-master gate.
		re, err := regexp.Compile(`\A(?:` + body + `)\z`)
		if err != nil {
			return onBadPattern
		}
		return slices.ContainsFunc(branches, re.MatchString)
	}
	return slices.Contains(branches, pattern)
}

// workflowRuns reports whether a workflow runs on a push by default. A
// workflow-level when:/unless: gates every job under it, so a workflow that is
// off by default holds no branch gates — and an opt-in extended workflow was
// otherwise free to supply the commands a developer is handed.
//
// Only the forms a checked-in config settles decide: a literal boolean, or a
// pipeline parameter whose declared default is one. A logic statement
// (and:/or:/equal:) or an unresolvable value keeps the workflow, because
// dropping the workflow that holds the real gates is the worse error.
func workflowRuns(w workflow, params map[string]string) bool {
	if v, known := conditionValue(w.When, params); known && !v {
		return false
	}
	if v, known := conditionValue(w.Unless, params); known && v {
		return false
	}
	return true
}

// conditionValue resolves a workflow when:/unless: to a boolean where it can.
// An absent field and a logic statement are both "unknown", which is why known
// is returned rather than folded into a default.
func conditionValue(n yaml.Node, params map[string]string) (value, known bool) {
	if n.Kind != yaml.ScalarNode {
		return false, false
	}
	s := strings.TrimSpace(n.Value)
	if m := pipelineParamRef.FindStringSubmatch(s); m != nil {
		v, ok := params[m[1]]
		if !ok {
			return false, false
		}
		s = v
	}
	return boolValue(s)
}

// pipelineParamRef matches a pipeline parameter interpolation that is the whole
// value — << pipeline.parameters.run-extended >> — the only form a workflow
// condition takes in practice.
var pipelineParamRef = regexp.MustCompile(`^<<\s*pipeline\.parameters\.([A-Za-z0-9_-]+)\s*>>$`)

// boolValue parses a YAML 1.1 boolean in the spellings a config may use, and
// reports whether the value was one at all.
func boolValue(s string) (value, known bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	}
	return false, false
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
		case s.Kind == kindRun:
			e.addRun(jc, s, args)

		case s.Kind == kindWhen || s.Kind == kindUnless:
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
// conditional on the job's outcome, a duplicate, or still carries an
// unsubstituted parameter in its command or its background flag.
func (e *extractor) addRun(jc jobCtx, s step, args map[string]string) {
	if !gatingWhen(substitute(s.When, args)) {
		return
	}
	// background is a string because a config may parameterize it. Whether the
	// step is a long-running process or a gate is then unknowable until the
	// value resolves, and unknowable is reported rather than assumed: guessing
	// "not a process" hands the user a daemon to run before every commit, and
	// guessing "process" drops a real gate while the notes claim nothing was
	// missed.
	if bg := strings.TrimSpace(substitute(s.Background, args)); bg != "" {
		background, known := boolValue(bg)
		if !known {
			e.res.Unresolved++
			return
		}
		if background {
			return
		}
	}
	cmd := strings.TrimSpace(substitute(s.Command, args))
	if cmd == "" {
		return
	}
	if unresolvedRef.MatchString(cmd) {
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

// gatingWhen reports whether a run step's when: makes it part of the gate.
// on_fail only runs on a red build, so it is diagnostic work — dumping logs,
// printing a failing test list — and letting it claim a role slot would
// displace the command that actually failed. always does run on green builds,
// but in practice marks cleanup and artifact upload rather than a check, so it
// is skipped too. Anything else, including the default on_success and an
// unrecognised value, is treated as a gate.
func gatingWhen(when string) bool {
	switch strings.TrimSpace(when) {
	case "on_fail", "always":
		return false
	}
	return true
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

// paramRef matches a job or command parameter interpolation, e.g.
// << parameters.x >>. These are the only references substitute resolves.
var paramRef = regexp.MustCompile(`<<\s*parameters\.([A-Za-z0-9_-]+)\s*>>`)

// unresolvedRef matches any CircleCI interpolation, deliberately wider than
// paramRef: << pipeline.parameters.x >>, << pipeline.git.branch >> and
// << matrix.os >> are resolved by CircleCI at pipeline time, never by us. A
// command still carrying one is not runnable — << is a bash heredoc operator,
// so passing it through yields a syntax error or a hang.
var unresolvedRef = regexp.MustCompile(`<<\s*[a-z]+\.`)

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

	// Parameters are the pipeline parameters the config declares. Their
	// defaults are what a workflow-level when:/unless: resolves against.
	Parameters map[string]parameter `yaml:"parameters"`

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

	// When and Unless stay nodes because either may be a boolean, a parameter
	// interpolation, or a logic statement mapping.
	When   yaml.Node `yaml:"when"`
	Unless yaml.Node `yaml:"unless"`
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
	Background string            // raw background value; may be a parameter reference
	When       string            // run step's when: on_success (default), on_fail, always
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
	case kindRun:
		s.decodeRun(value)
	case kindWhen, kindUnless:
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
//
// Fields are read one at a time rather than decoded into a struct, because a
// single value that does not fit its Go type fails the whole decode and takes
// the command with it. `background: << parameters.daemon >>` is not a bool and
// `name: 3` is not a string, and either made an entire test step vanish with
// nothing recorded to say so.
func (s *step) decodeRun(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		s.Command = n.Value
		return
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		v := n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			continue
		}
		switch n.Content[i].Value {
		case "name":
			s.Name = v.Value
		case "command":
			s.Command = v.Value
		case "working_directory":
			s.WorkingDir = v.Value
		// A run step's own when: is its on_success/on_fail condition, not the
		// step kind that wraps other steps; they share the word, not a meaning.
		case kindWhen:
			s.When = v.Value
		case "background":
			s.Background = v.Value
		}
	}
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
