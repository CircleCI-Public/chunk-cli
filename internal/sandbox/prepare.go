package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const maxBuildRetries = 3

type packageManager struct {
	name           string
	installCommand string
	lockfile       string
}

type requiredCredential struct {
	BuildArg    string `json:"buildArg"`
	Description string `json:"description"`
	Sensitive   bool   `json:"sensitive"`
}

// Prepare generates a Dockerfile, builds it, and runs tests inside the container.
func Prepare(ctx context.Context, claude *anthropic.Client, io iostream.Streams, stdin io.Reader) error {
	io.ErrPrintln(ui.Dim("preparing..."))

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	repoContext := gatherRepoContext(cwd)
	existingDockerfiles := gatherDockerfiles(cwd)
	pm := detectPackageManager(cwd)
	if pm != nil {
		io.ErrPrintf("%s\n", ui.Dim(fmt.Sprintf("detected package manager: %s (%s)", pm.name, pm.lockfile)))
	}

	model := config.DefaultModel

	// Identify required credentials
	io.ErrPrintln(ui.Dim("scanning for private dependencies..."))
	credentials, err := identifyCredentials(ctx, claude, model, repoContext, existingDockerfiles, io, stdin)
	if err != nil {
		return err
	}

	// Determine test command
	testCommand, err := determineTestCommand(ctx, claude, model, repoContext, pm)
	if err != nil {
		return err
	}
	if testCommand == "" {
		return fmt.Errorf("could not determine test command")
	}
	io.ErrPrintf("test command: %s\n", ui.Bold(testCommand))

	// Identify base image and fetch tags
	io.ErrPrintln(ui.Dim("resolving base image tags..."))
	baseImageRepo, err := identifyBaseImage(ctx, claude, model, repoContext, testCommand)
	if err != nil {
		return err
	}
	var tags []string
	if baseImageRepo != "" {
		tags, _ = fetchDockerHubTags(baseImageRepo)
		io.ErrPrintf("  %s: %s\n", baseImageRepo, ui.Dim(fmt.Sprintf("%d tags fetched from Docker Hub", len(tags))))
	}

	// Generate Dockerfile
	io.ErrPrintln(ui.Dim("generating Dockerfile..."))
	dockerfileContent, err := generateDockerfile(ctx, claude, model, testCommand, repoContext, existingDockerfiles, baseImageRepo, tags, credentials, pm)
	if err != nil {
		return err
	}
	if dockerfileContent == "" {
		return fmt.Errorf("could not generate Dockerfile")
	}

	// Write Dockerfile
	dockerfileName := uniqueDockerfileName(cwd)
	dockerfilePath := filepath.Join(cwd, dockerfileName)
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dockerfileName, err)
	}
	io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("wrote %s", dockerfileName)))

	// Build and test
	success, err := buildAndTest(ctx, claude, model, cwd, dockerfileName, dockerfilePath, dockerfileContent, testCommand, repoContext, credentials, pm, io)
	if err != nil {
		return err
	}
	if !success {
		return fmt.Errorf("prepare failed")
	}
	return nil
}

func identifyCredentials(ctx context.Context, claude *anthropic.Client, model, repoContext, existingDockerfiles string, io iostream.Streams, stdin io.Reader) (map[string]string, error) {
	resp, err := claude.Ask(ctx, model, 512, credentialsPrompt(repoContext, existingDockerfiles))
	if err != nil {
		return nil, fmt.Errorf("identify credentials: %w", err)
	}

	stripped := stripMarkdownFences(resp)
	var creds []requiredCredential
	if err := json.Unmarshal([]byte(stripped), &creds); err != nil {
		return nil, nil // not fatal — treat as no credentials needed
	}

	collected := make(map[string]string)
	if len(creds) == 0 {
		return collected, nil
	}

	io.ErrPrintf("\nFound %d credential(s) needed:\n\n", len(creds))
	for _, c := range creds {
		io.ErrPrintf("  %s: %s\n", ui.Bold(c.BuildArg), ui.Dim(c.Description))
	}
	io.ErrPrintln("")

	scanner := bufio.NewScanner(stdin)
	for _, c := range creds {
		if c.Sensitive {
			value, err := tui.PromptHidden(c.BuildArg)
			if err != nil {
				return nil, fmt.Errorf("reading credential %s: %w", c.BuildArg, err)
			}
			collected[c.BuildArg] = value
		} else {
			io.ErrPrintf("%s: ", c.BuildArg)
			if !scanner.Scan() {
				return nil, fmt.Errorf("reading credential %s: %w", c.BuildArg, scanner.Err())
			}
			collected[c.BuildArg] = scanner.Text()
		}
	}
	io.ErrPrintln("")
	return collected, nil
}

func determineTestCommand(ctx context.Context, claude *anthropic.Client, model, repoContext string, pm *packageManager) (string, error) {
	resp, err := claude.Ask(ctx, model, 256, testCommandPrompt(repoContext, pm))
	if err != nil {
		return "", fmt.Errorf("determine test command: %w", err)
	}
	return strings.TrimSpace(resp), nil
}

func identifyBaseImage(ctx context.Context, claude *anthropic.Client, model, repoContext, testCommand string) (string, error) {
	resp, err := claude.Ask(ctx, model, 64, baseImagePrompt(repoContext, testCommand))
	if err != nil {
		return "", fmt.Errorf("identify base image: %w", err)
	}

	stripped := stripMarkdownFences(resp)
	var parsed struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal([]byte(stripped), &parsed); err != nil {
		return "", nil // not fatal
	}
	return parsed.Repository, nil
}

// validRepoComponent matches a valid Docker Hub namespace or image name.
var validRepoComponent = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func fetchDockerHubTags(repository string) ([]string, error) {
	namespace, image, _ := strings.Cut(repository, "/")
	if image == "" {
		image = namespace
		namespace = "library"
	}

	// Validate components to prevent path traversal or query injection from LLM output.
	if !validRepoComponent.MatchString(namespace) || !validRepoComponent.MatchString(image) {
		return nil, fmt.Errorf("invalid Docker Hub repository: %s", repository)
	}

	var tags []string
	url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/tags/?page_size=100", namespace, image)

	for page := 0; page < 3 && url != ""; page++ {
		resp, err := http.Get(url) //nolint:gosec // Docker Hub public API
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			break
		}

		var data struct {
			Next    *string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			_ = resp.Body.Close()
			break
		}
		_ = resp.Body.Close()

		for _, r := range data.Results {
			if !strings.HasPrefix(r.Name, "sha256:") && r.Name != "latest" {
				tags = append(tags, r.Name)
			}
		}

		if data.Next != nil {
			url = *data.Next
		} else {
			url = ""
		}
	}
	return tags, nil
}

func generateDockerfile(ctx context.Context, claude *anthropic.Client, model, testCommand, repoContext, existingDockerfiles, baseImageRepo string, tags []string, credentials map[string]string, pm *packageManager) (string, error) {
	resp, err := claude.Ask(ctx, model, 1024, dockerfilePrompt(testCommand, repoContext, existingDockerfiles, baseImageRepo, tags, credentials, pm))
	if err != nil {
		return "", fmt.Errorf("generate Dockerfile: %w", err)
	}
	return strings.TrimSpace(resp), nil
}

func buildAndTest(ctx context.Context, claude *anthropic.Client, model, cwd, dockerfileName, dockerfilePath, dockerfileContent, testCommand, repoContext string, credentials map[string]string, pm *packageManager, io iostream.Streams) (bool, error) {
	imageTag := "chunk-prep"

	buildArgs := make([]string, 0, len(credentials)*2)
	for k, v := range credentials {
		buildArgs = append(buildArgs, "--build-arg", k+"="+v)
	}

	// Create build context from git archive
	buildContext, err := os.MkdirTemp("", "chunk-build-")
	if err != nil {
		return false, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(buildContext) }()

	archiveCmd := exec.Command("git", "archive", "HEAD")
	archiveCmd.Dir = cwd
	archive, err := archiveCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git archive: %w", err)
	}

	tarCmd := exec.Command("tar", "-x", "-C", buildContext)
	tarCmd.Stdin = strings.NewReader(string(archive))
	if err := tarCmd.Run(); err != nil {
		return false, fmt.Errorf("extract archive: %w", err)
	}

	currentContent := dockerfileContent

	for attempt := 0; attempt <= maxBuildRetries; attempt++ {
		if attempt > 0 {
			io.ErrPrintf("\n%s\n", ui.Yellow(fmt.Sprintf("fixing Dockerfile (attempt %d of %d)...", attempt, maxBuildRetries)))
			if err := os.WriteFile(dockerfilePath, []byte(currentContent+"\n"), 0o644); err != nil {
				return false, fmt.Errorf("write %s: %w", dockerfileName, err)
			}
		}
		if err := os.WriteFile(filepath.Join(buildContext, dockerfileName), []byte(currentContent+"\n"), 0o644); err != nil {
			return false, fmt.Errorf("write %s to build context: %w", dockerfileName, err)
		}

		// Build
		io.ErrPrintf("\n%s\n", ui.Dim(fmt.Sprintf("building %s...", dockerfileName)))
		buildCmdArgs := []string{"build", "-f", dockerfileName, "-t", imageTag}
		buildCmdArgs = append(buildCmdArgs, buildArgs...)
		buildCmdArgs = append(buildCmdArgs, ".")

		buildExec := exec.CommandContext(ctx, "docker", buildCmdArgs...)
		buildExec.Dir = buildContext
		buildOutput, buildErr := buildExec.CombinedOutput()

		if len(buildOutput) > 0 {
			_, _ = fmt.Fprint(io.Err, string(buildOutput))
		}

		if buildErr != nil {
			if attempt < maxBuildRetries {
				io.ErrPrintln(ui.Yellow("Docker build failed. Asking Claude to fix the Dockerfile..."))
				fixed, err := claude.Ask(ctx, model, 1024, dockerfileFixPrompt(currentContent, string(buildOutput), testCommand, repoContext, pm))
				if err == nil && fixed != "" {
					currentContent = strings.TrimSpace(fixed)
				}
				continue
			}
			return false, fmt.Errorf("docker build failed, check %s for issues", dockerfileName)
		}

		// Run tests
		io.ErrPrintln("\n" + ui.Dim("running test command in container..."))
		runCmdArgs := []string{"run", "--rm", imageTag, "sh", "-c", testCommand}

		runExec := exec.CommandContext(ctx, "docker", runCmdArgs...)
		runExec.Dir = cwd
		runOutput, runErr := runExec.CombinedOutput()

		if len(runOutput) > 0 {
			_, _ = fmt.Fprint(io.Out, string(runOutput))
		}

		if runErr == nil {
			return true, nil
		}

		if attempt < maxBuildRetries {
			io.ErrPrintln(ui.Yellow("Tests failed. Asking Claude to fix the Dockerfile..."))
			fixed, err := claude.Ask(ctx, model, 1024, dockerfileFixPrompt(currentContent, string(runOutput), testCommand, repoContext, pm))
			if err == nil && fixed != "" {
				currentContent = strings.TrimSpace(fixed)
			}
		} else {
			return false, fmt.Errorf("tests failed inside the container")
		}
	}

	return false, nil
}

func gatherRepoContext(cwd string) string {
	var parts []string

	// Root file listing
	entries, err := os.ReadDir(cwd)
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		parts = append(parts, "Root files:\n"+strings.Join(names, "\n"))
	}

	candidates := []string{
		"package.json", "Makefile", "go.mod", "pom.xml",
		"build.gradle", "build.gradle.kts", "pyproject.toml",
		"setup.py", "pytest.ini", "Cargo.toml",
		".chunk/config.json",
		".npmrc", ".yarnrc", ".yarnrc.yml",
		"pip.conf", ".pip/pip.conf", ".cargo/config.toml",
		"settings.xml", "gradle.properties",
		"requirements.txt", "requirements-dev.txt", "requirements-test.txt",
		"Pipfile", "Gemfile", "go.sum",
		"project.clj", "deps.edn", "build.clj", "profiles.clj",
	}

	for _, rel := range candidates {
		full := filepath.Join(cwd, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 4000 {
			content = content[:4000]
		}
		parts = append(parts, fmt.Sprintf("\n--- %s ---\n%s", rel, content))
	}

	return strings.Join(parts, "\n")
}

func gatherDockerfiles(cwd string) string {
	var parts []string

	searchDirs := []string{".", "docker", ".docker", "build", "ci", ".circleci", "infra", "deploy"}

	for _, dir := range searchDirs {
		dirPath := filepath.Join(cwd, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			lower := strings.ToLower(name)
			if !strings.HasPrefix(lower, "dockerfile") || strings.HasPrefix(name, "Dockerfile.chunk") || strings.HasPrefix(name, "Dockerfile.sandbox") {
				continue
			}
			rel := name
			if dir != "." {
				rel = dir + "/" + name
			}
			data, err := os.ReadFile(filepath.Join(cwd, rel))
			if err != nil {
				continue
			}
			content := string(data)
			if len(content) > 4000 {
				content = content[:4000]
			}
			parts = append(parts, fmt.Sprintf("\n--- %s ---\n%s", rel, content))
		}
	}

	// Compose files
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 4000 {
			content = content[:4000]
		}
		parts = append(parts, fmt.Sprintf("\n--- %s ---\n%s", name, content))
	}

	return strings.Join(parts, "\n")
}

func detectPackageManager(cwd string) *packageManager {
	managers := []packageManager{
		{lockfile: "pnpm-lock.yaml", name: "pnpm", installCommand: "pnpm install"},
		{lockfile: "yarn.lock", name: "yarn", installCommand: "yarn install --frozen-lockfile"},
		{lockfile: "bun.lock", name: "bun", installCommand: "bun install --frozen-lockfile"},
		{lockfile: "bun.lockb", name: "bun", installCommand: "bun install --frozen-lockfile"},
		{lockfile: "package-lock.json", name: "npm", installCommand: "npm ci"},
	}

	for _, m := range managers {
		if _, err := os.Stat(filepath.Join(cwd, m.lockfile)); err == nil {
			return &m
		}
	}
	return nil
}

func uniqueDockerfileName(cwd string) string {
	name := "Dockerfile.chunk"
	counter := 1
	for {
		if _, err := os.Stat(filepath.Join(cwd, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("Dockerfile.chunk.%d", counter)
		counter++
	}
}

func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove first line (```json or similar)
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
