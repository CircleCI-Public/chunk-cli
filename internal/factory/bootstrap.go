package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/harness"
	"github.com/CircleCI-Public/chunk-cli/internal/harness/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// BootstrapPoolName names the pool bootstrap runs on. It is its own pool, not
// the review pool: bootstrap needs one sidecar, and asking the review pool for
// one would delete the rest of it.
const BootstrapPoolName = "factory-bootstrap"

// BootstrapImage is the sidecar image bootstrap runs on unless told otherwise:
// a system template with Claude Code installed. Bootstrap only reads the
// project, so it needs Claude rather than the project's toolchain, and the
// project's own image may not have Claude. The provisioner has no alias for
// the newest template, so this names one and needs bumping as they are added.
const BootstrapImage = "cimg-base:2026.09-claude"

// DefaultBootstrapTimeout bounds writing the review prompts. Claude reads
// widely around the repository before it writes anything, and on a large one
// that takes longer than a review does.
const DefaultBootstrapTimeout = 30 * time.Minute

// StandardsPath is where build-prompt writes the team's review standards, mined
// from real review comments. Bootstrap reads it when it is there.
const StandardsPath = ".chunk/context/review-prompt.md"

// Limits on the prompts accepted from the model. Its answer is untrusted text
// written to the repository, so a runaway answer must not write an unbounded
// amount, or a name that is not a plain file name.
const (
	maxBootstrapPrompts = 6
	maxPromptBody       = 32 * 1024
	maxStandards        = 64 * 1024
	// maxResultInError caps how much of claude's own result an error quotes.
	maxResultInError = 500
)

// bootstrapTools lets the agent read the repository and its history, and
// nothing else: it writes the prompts in its answer, not to the tree.
var bootstrapTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git log:*)", "Bash(git show:*)", "Bash(git ls-files:*)",
}

// promptNameRe is a review prompt's file name without its extension.
var promptNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// bootstrapSchema is the shape of the agent's answer, passed to claude with
// --json-schema.
const bootstrapSchema = `{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "A few sentences on what you learned about the repository and why you chose these review areas."
    },
    "reviews": {
      "type": "array",
      "minItems": 1,
      "maxItems": 6,
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "description": "File name without extension, lowercase words joined by hyphens, such as \"testing\"."},
          "body": {"type": "string", "description": "The review prompt, as markdown."}
        },
        "required": ["name", "body"],
        "additionalProperties": false
      }
    }
  },
  "required": ["summary", "reviews"],
  "additionalProperties": false
}`

// bootstrapInstructions is what the agent is asked to do. %s is the validation
// commands section and %s the team standards section.
const bootstrapInstructions = `You are setting up code review for this repository. An automated loop makes changes here: an implementer agent writes each change, then independent reviewers check it, one per review prompt, and their findings are sent back to the implementer until the change passes. Your job is to write those review prompts.

## How the prompts are used

- A reviewer is given one prompt, together with the requested change, which is the specification, and an instruction to read the uncommitted change with ` + "`git diff HEAD`" + `. Write only the review instructions; do not repeat those parts.
- A reviewer can read the repository and its history but cannot edit files or run commands such as tests.
- A reviewer reports findings as structured data, each with a severity: high, medium, low or info. Do not describe an output format.
- Every high or medium finding sends the change back for another round of work. A prompt that produces high or medium findings for things not worth fixing makes the loop slower and its results worse. So every prompt must say precisely what clears the high and medium bar in its area, and send everything else to low.
%s
## What to read first

Read what the repository says about itself before you write anything: AGENTS.md, CLAUDE.md, CONTRIBUTING and README files, docs, CI configuration, linter and formatter configuration, and a sample of the code and its tests. Use git log to see how recent changes are made. Work out the languages and frameworks, the architectural rules, and the error handling and testing conventions the project actually follows.
%s
## What to write

- Write 2 to 4 prompts. Each covers a distinct area, with no overlap: any finding should belong to exactly one prompt. Choose the areas this repository needs, such as correctness of the core logic, tests, architecture and conventions, or security, and leave out any area you have nothing specific to say about.
- Each prompt has:
  - A title, and a short paragraph naming the project and what this reviewer looks for.
  - The repository's own rules for the area, made concrete from what you read, with file paths and the patterns the code uses. Leave out generic advice a competent reviewer already knows.
  - A severity section that defines high, medium and low for the area. A high or medium finding must come with a concrete failure the reviewer can describe: the input or state, and what goes wrong.
  - These instructions: review only the change, not code it does not touch, unless the change makes a problem newly reachable; treat the requested change as the scope, so behavior it does not ask for is not required; report all the findings at once rather than holding related ones back for a later round; and report no findings when nothing clears the bar.
- Keep each prompt under about 80 lines of markdown.
- Give each prompt a name: its file name without an extension, in lowercase words joined by hyphens, such as "testing".
- In the summary, say in a few sentences what you learned about the repository and why you chose these areas.`

// BootstrapOptions configures writing a project's first review prompts.
type BootstrapOptions struct {
	Exec       harness.Execer
	Entry      *sidecar.PoolEntry
	Credential claudecode.Credential
	BaseURL    string
	Model      string        // optional; Claude Code's default when empty
	Timeout    time.Duration // DefaultBootstrapTimeout when zero
	// Commands are the project's validation commands, which run on every
	// change, so the reviews need not check what they already enforce.
	Commands []config.Command
	// Standards is the team's review standards from build-prompt, if any.
	Standards string
}

// Bootstrap is the outcome of writing a project's first review prompts.
type Bootstrap struct {
	Prompts []GeneratedPrompt
	// Summary is the agent's account of the repository and its choices.
	Summary string
	// Dropped counts prompts in the answer that were unusable: a bad or
	// repeated name, an empty or oversized body, or past the limit.
	Dropped int
}

// GeneratedPrompt is one review prompt bootstrap wrote.
type GeneratedPrompt struct {
	Name string
	Body string
}

// RunBootstrap has Claude Code read the repository on the entry's sidecar and
// write review prompts for it. It writes nothing locally; see WritePrompts.
func RunBootstrap(ctx context.Context, opts BootstrapOptions) (Bootstrap, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultBootstrapTimeout
	}
	turn, err := claudecode.Run(ctx, opts.Exec, opts.Entry, bootstrapPrompt(opts.Commands, opts.Standards), claudecode.Options{
		Credential: opts.Credential,
		BaseURL:    opts.BaseURL,
		Model:      opts.Model,
		Timeout:    timeout,
		Tools:      bootstrapTools,
		Schema:     bootstrapSchema,
	})
	if err != nil {
		return Bootstrap{}, err
	}
	return parseBootstrap(turn.Output)
}

// bootstrapPrompt fills in the instructions with what the project already has.
func bootstrapPrompt(commands []config.Command, standards string) string {
	var checks string
	if cmds := ValidationCommands(commands); len(cmds) > 0 {
		var b strings.Builder
		b.WriteString("- These validation commands also run on every change, and their failures are sent back too, so reviews must not check what they enforce, such as formatting, lint rules, build errors or failing tests:\n")
		for _, c := range cmds {
			fmt.Fprintf(&b, "  - %s: `%s`\n", c.Name, c.Run)
		}
		checks = b.String()
	}
	var team string
	if s := strings.TrimSpace(standards); s != "" {
		if len(s) > maxStandards {
			s = strings.ToValidUTF8(s[:maxStandards], "")
		}
		team = "\nThe team's review standards, mined from comments on its past pull requests, are in `" + StandardsPath +
			"` and below. Treat them as evidence of what this team pushes back on, and tell reviewers to read that file.\n\n<standards>\n" +
			s + "\n</standards>\n"
	}
	return fmt.Sprintf(bootstrapInstructions, checks, team)
}

// bootstrapAnswer is the agent's answer as bootstrapSchema shapes it.
type bootstrapAnswer struct {
	Summary string `json:"summary"`
	Reviews []struct {
		Name string `json:"name"`
		Body string `json:"body"`
	} `json:"reviews"`
}

// parseBootstrap reads the prompts out of claude's JSON result. Claude Code has
// checked the answer's shape against the schema; the names and sizes are still
// checked here, since they become files.
func parseBootstrap(output string) (Bootstrap, error) {
	res, err := claudecode.ParseResult(output)
	if err != nil {
		return Bootstrap{}, err
	}
	if res.IsError || res.StructuredOutput == nil {
		return Bootstrap{}, fmt.Errorf("claude gave no review prompts (%s): %s", res.Subtype, review.Tail(strings.TrimSpace(res.Result), maxResultInError))
	}
	var answer bootstrapAnswer
	if err := json.Unmarshal(res.StructuredOutput, &answer); err != nil {
		return Bootstrap{}, fmt.Errorf("read claude's review prompts: %w", err)
	}
	out := Bootstrap{Summary: strings.TrimSpace(answer.Summary)}
	seen := map[string]bool{}
	for _, r := range answer.Reviews {
		name, body := strings.TrimSpace(r.Name), strings.TrimSpace(r.Body)
		if len(out.Prompts) >= maxBootstrapPrompts || !promptNameRe.MatchString(name) || seen[name] ||
			body == "" || len(body) > maxPromptBody {
			out.Dropped++
			continue
		}
		seen[name] = true
		out.Prompts = append(out.Prompts, GeneratedPrompt{Name: name, Body: body})
	}
	if len(out.Prompts) == 0 {
		return out, errors.New("claude gave no usable review prompts")
	}
	return out, nil
}

// ErrPromptsExist means the directory bootstrap would write to already has
// review prompts, which it never overwrites.
var ErrPromptsExist = errors.New("review prompts already exist")

// CheckPromptsDir reports ErrPromptsExist when dir already has review prompts,
// so a caller can refuse before any sidecar starts. A missing dir is fine.
func CheckPromptsDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".md" || ext == ".txt") {
			return fmt.Errorf("%w in %s", ErrPromptsExist, dir)
		}
	}
	return nil
}

// WritePrompts writes each prompt to dir as <name>.md and returns the paths
// written. It refuses a dir that already has review prompts, checked again
// here because time passes between the caller's check and the answer.
func WritePrompts(dir string, prompts []GeneratedPrompt) ([]string, error) {
	if err := CheckPromptsDir(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	paths := make([]string, 0, len(prompts))
	for _, p := range prompts {
		path := filepath.Join(dir, p.Name+".md")
		// O_EXCL so a file that appeared since the check is not overwritten.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return paths, fmt.Errorf("write %s: %w", path, err)
		}
		_, werr := f.WriteString(p.Body + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return paths, fmt.Errorf("write %s: %w", path, werr)
		}
		paths = append(paths, path)
	}
	return paths, nil
}
