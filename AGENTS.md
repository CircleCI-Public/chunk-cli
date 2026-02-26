# Chunk CLI

Internal CLI tool for generating AI agent context from real PR review patterns using Claude.

## Project Structure

### Core Directories
- `src/` - TypeScript source code
  - `index.ts` - Main entry point, command routing
  - `commands/` - Command implementations (build-prompt, auth, config, upgrade)
  - `core/` - Core business logic (build-prompt orchestration, agent)
  - `review_prompt_mining/` - Pipeline: reviewer discovery, analysis, context generation
  - `config/` - Configuration system (models, paths)
  - `storage/` - Persistence layer (config files)
  - `ui/` - Terminal UI utilities (colors, spinner, formatting)
  - `types/` - TypeScript type definitions
  - `utils/` - Utilities (errors)
  - `__tests__/` - Test suite (unit tests)

### Configuration Files
- `package.json` - Dependencies and scripts
- `tsconfig.json` - TypeScript configuration (strict mode)
- `.mise.toml` - Runtime version management (Bun 1.3, Node 24)
- `install.sh` - Installation script (binary download with source build fallback)
- `scripts/` - Build and release automation

### Documentation
- `README.md` - User-facing documentation
## Architecture

The TypeScript implementation follows a layered architecture with clear separation of concerns:

### Commands Layer
Entry points for CLI commands. Each command:
- Parses arguments and validates input
- Delegates business logic to core services

### Core Layer
Business logic and domain services:
- **Build Prompt** (`src/core/build-prompt.ts`): Orchestrates the three-step context generation pipeline
- **Agent** (`src/core/agent.ts`): Anthropic API client and key validation
- **Review Prompt Mining** (`src/review_prompt_mining/`): GitHub reviewer discovery, comment analysis, and context generation

### Storage Layer
Persistent data management:
- User config at `~/.config/chunk/config.json`
- Secure file permissions (0600)

### Configuration System
Multi-source configuration with precedence:
1. CLI arguments (highest priority)
2. Environment variables
3. User-level config
4. Built-in defaults (lowest priority)

## Installation Design

### Binary Download (Preferred)
- `install.sh` first tries `gh release download` from GitHub Releases
- Requires `gh` CLI installed and authenticated — handles private repo auth automatically
- Downloads platform-specific binary, verifies with `--version`, installs atomically
- Falls back to source build if `gh` is unavailable, unauthenticated, or download fails
- `--branch` flag forces source build (for development use)

### Source-Based Installation (Fallback)
- Installation builds from source using Bun
- Bun 1.3 required (auto-installed via mise if available)
- Platform-specific builds for darwin-arm64, darwin-x64, linux-arm64, and linux-x64
- Build artifacts created in `dist/` directory (gitignored)
- Installed binary at `~/.local/bin/chunk`
- No pre-built binaries in repository (prevents git issues)

### Repo Clone
- `~/.local/share/chunk/repo` is always cloned/updated regardless of install method
- Required for team prompts, upgrade command, and `install.sh` itself

### Self-Update Mechanism
- `chunk upgrade` runs `install.sh`, which tries binary download first, then source build
- Git pull + download/rebuild + replace binary atomically
- Uses semantic versioning (x.y.z) from package.json
- Preserves user configuration during upgrades

### Build Process
- `bun run build` compiles TypeScript to native binaries
- `scripts/build.ts` handles multi-platform compilation
- Platform-specific builds available for faster installation

## Development Workflow

### Running from Source
```bash
# Run with development mode (hot reload)
bun run dev build-prompt --org <org>

# Run specific command
bun run dev auth login
```

### Type Checking
```bash
# Check types without building
bun run typecheck
```

### Building Binaries
```bash
# Build for current platform
bun run build

# Output: dist/chunk-darwin-arm64
```

### Testing
```bash
bun test                                    # All tests
```

### Releasing
```bash
# 1. Bump version in package.json
# 2. Build all platform binaries
bun run build

# 3. Validate everything (no side effects)
bun run release --dry-run

# 4. Create release (tag + upload to GitHub Releases)
bun run release

# Or create as draft first for review
bun run release --draft
```

The release script (`scripts/release.ts`):
- Reads version from `package.json`, derives tag `v{version}`
- Preflight checks: `gh` authenticated, clean working dir, on `main`, tag doesn't exist
- Validates all 4 platform binaries exist in `dist/` with reasonable sizes
- Generates SHA-256 checksums → `dist/checksums.txt`
- Creates annotated git tag, pushes it
- Runs `gh release create` with binaries, checksums, and release notes

## Code Conventions

### TypeScript
- Strict mode enabled in tsconfig.json
- Type-first development: define types before implementation
- No `any` types unless absolutely necessary
- Prefer interfaces for public APIs, types for internal use

### Project Organization
- Path aliases: `@/*` maps to `src/*`
- One component/service per file
- Index files for clean exports
- Separation of concerns: commands, core, storage layers

### Error Handling
- Typed error classes (NetworkError, AuthError, GitError)
- Errors bubble up to command layer for display
- User-friendly error messages with actionable suggestions

### Testing Philosophy
- Unit tests for pure functions and utilities
- No Claude API mocking - tests requiring API key skip gracefully if missing

### Code Style
- Use async/await over promises
- Prefer const over let
- Destructure objects and arrays
- Use template literals for string interpolation
- Early returns to reduce nesting

## Data Storage

| Path | Purpose |
|------|---------|
| `~/.config/chunk/config.json` | User configuration |

All files created with mode `0600` (owner read/write only).
