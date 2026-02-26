# chunk

CLI for generating AI agent context from real code review patterns. Mines PR review comments from GitHub, analyzes them with Claude, and outputs a context prompt file tuned to your team's standards.

## Features

- **Pattern Mining** - Discovers top reviewers in your GitHub org and fetches their review comments
- **AI Analysis** - Uses Claude (Sonnet, Opus, or Haiku) to identify recurring patterns and team standards
- **Context Generation** - Produces a markdown prompt file ready to use with AI coding agents
- **Self-Updating** - Built-in upgrade command for binary updates

## Requirements

- **macOS** (arm64 or x86_64) or **Linux** (arm64 or x86_64)
- **Fast path**: `gh` CLI installed and authenticated (`gh auth login`)
- **Fallback**: Bun 1.3+ (auto-installed via mise if available)

## Installation

To install the `chunk` CLI, ensure that `gh` (the Github CLI) is installed and auth is set up,
then run the following:

```bash
gh api -H "Accept: application/vnd.github.v3.raw" "/repos/circleci/code-review-cli/contents/install.sh"| bash
```

This will check that `~/.local/bin` is in your `$PATH` and will warn you if you need to add it manually.

You can confirm the tool is installed by running:

```bash
chunk --version
```

### Building a local development version

To build and install from source:

```bash
./install-local.sh
```

## Quick Start

```bash
# Authenticate with your Anthropic API key
chunk auth login

# Generate a context prompt from your org's review patterns
chunk build-prompt --org myorg

# Use the generated context with an AI coding agent
# Place the output file in .chunk/context/ or pass it via --config
```

## Commands

### build-prompt

Mines PR review comments from GitHub to generate a custom review prompt tuned to your team's patterns. Runs a three-step pipeline:

1. **Discover** — finds the top reviewers in your org by review activity
2. **Analyze** — sends their comments to Claude to identify recurring patterns
3. **Generate** — produces a markdown prompt file that agents can use for context

```bash
chunk build-prompt --org <org> [options]
```

**Required:**

| Flag | Description |
|------|-------------|
| `--org <org>` | GitHub organization to analyze |

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `--repos <repos>` | all repos in org | Comma-separated list of repo names to include |
| `--top <n>` | `5` | Number of top reviewers to analyze |
| `--since <date>` | 3 months ago | Start date in `YYYY-MM-DD` format |
| `--output <path>` | `./pr-review-prompt.md` | Output path for the generated prompt |
| `--max-comments <n>` | all | Max comments per reviewer sent for analysis |
| `--analyze-model <model>` | `claude-sonnet-4-5-20250929` | Claude model for the analysis step |
| `--prompt-model <model>` | `claude-opus-4-5-20251101` | Claude model for prompt generation |
| `--include-attribution` | off | Include reviewer names in the generated prompt |

**Environment variables required:**

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | GitHub personal access token with `repo` scope |
| `ANTHROPIC_API_KEY` | Anthropic API key |

**Output files** (written alongside the final prompt):

| File | Description |
|------|-------------|
| `<output>-details.json` | Raw review comments for the top reviewers |
| `<output>-analysis.md` | Claude's analysis of reviewer patterns |
| `<output>.md` | Final generated prompt |

**Examples:**

```bash
# Analyze all repos in an org
chunk build-prompt --org myorg

# Analyze specific repos, keeping the top 10 reviewers
chunk build-prompt --org myorg --repos api,backend,frontend --top 10

# Limit data sent to Claude and save output to a specific path
chunk build-prompt --org myorg --repos myrepo --max-comments 50 --output ./prompts/review.md
```

Once generated, place the output file in `.chunk/context/` so AI coding agents (e.g., Claude Code) automatically pick it up as context.

### Other Commands

```bash
chunk auth login      # Set up API key
chunk auth status     # Check authentication
chunk config show     # Display current configuration
chunk upgrade         # Update to latest version
```

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GITHUB_TOKEN` | GitHub personal access token (required for `build-prompt`) |

## Platform Support

| Platform | Status |
|----------|--------|
| macOS (Apple Silicon) | Supported |
| macOS (Intel) | Supported |
| Linux (arm64) | Supported |
| Linux (x86_64) | Supported |
| Windows | Not supported |

## Model Pricing

| Model | Input | Output |
|-------|-------|--------|
| claude-opus-4-6 | $5/M | $25/M |
| claude-opus-4-5-20251101 | $5/M | $25/M |
| claude-sonnet-4-5-20250929 | $3/M | $15/M |
| claude-sonnet-4-20250514 | $3/M | $15/M |
| claude-haiku-4-5-20251001 | $1/M | $5/M |
| claude-haiku-3-5-20241022 | $0.80/M | $4/M |

Cache tokens: writes 1.25x, reads 0.1x base input price.

## Development

### Building

```bash
bun run build          # Build binaries for all platforms → dist/
bun run typecheck      # Type check without building
bun test               # Run test suite
```

### Releasing

Requires `gh` CLI authenticated (`gh auth login`).

```bash
# 1. Bump version in package.json
# 2. Build all platform binaries
bun run build

# 3. Validate (no side effects)
bun run release --dry-run

# 4. Tag + upload to GitHub Releases
bun run release

# Or create as draft for review first
bun run release --draft
```

The release script validates preflight checks (clean working dir, on `main`, tag available), generates SHA-256 checksums, creates an annotated git tag, and uploads all binaries to GitHub Releases.

---

See [AGENTS.md](AGENTS.md) for architecture and development documentation.
