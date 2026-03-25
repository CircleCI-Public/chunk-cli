package sandbox

import (
	"fmt"
	"strings"
)

func credentialsPrompt(context, existingDockerfiles string) string {
	var b strings.Builder
	b.WriteString("You are analyzing a software repository to identify private or non-public dependencies that require authentication credentials at build or install time.\n\n")
	b.WriteString("Repository context:\n")
	b.WriteString(context)
	b.WriteString("\n\n")
	if existingDockerfiles != "" {
		b.WriteString("Existing Dockerfiles:\n")
		b.WriteString(existingDockerfiles)
		b.WriteString("\n\n")
	}
	b.WriteString(`Look carefully for:
- Private npm registries (S3-backed, Artifactory, GitHub Packages, Verdaccio, etc.)
- Private PyPI indexes or --extra-index-url references
- Private Maven or Gradle repositories requiring credentials
- Private Cargo registries
- Private Go module proxies or GONOSUMCHECK patterns
- Any other indication that a dependency is fetched from a non-public source

For each required credential output a JSON array. Each element must have:
  "buildArg": the Docker ARG name (e.g. "AWS_ACCESS_KEY_ID", "NPM_TOKEN")
  "description": what it is and why it is needed
  "sensitive": true if it is a secret/token/password, false otherwise

Output ONLY the JSON array. If nothing private is detected, output [].`)
	return b.String()
}

func testCommandPrompt(context string, pm *packageManager) string {
	var b strings.Builder
	b.WriteString("You are analyzing a software repository to determine how tests are run.\n\n")
	if pm != nil {
		fmt.Fprintf(&b, "Detected package manager: %s (lockfile: %s). Use %s to run tests (e.g. `%s test`).\n\n",
			pm.name, pm.lockfile, pm.name, pm.name)
	}
	b.WriteString(context)
	b.WriteString("\n\nBased on the above, output ONLY the shell command used to run the test suite — nothing else. No explanation, no markdown. Just the command string.")
	return b.String()
}

func baseImagePrompt(context, testCommand string) string {
	return fmt.Sprintf(`You are selecting a Docker base image for a software project.

Test command: %s

Repository context:
%s

Output ONLY a JSON object with a single field "repository": the Docker Hub repository name for the most appropriate base image (e.g. "clojure", "node", "python", "golang", "rust"). For official images use just the name. For third-party images use "namespace/image". No tag — just the repository name. No explanation, no markdown.`, testCommand, context)
}

func dockerfilePrompt(testCommand, context, existingDockerfiles string, baseImageRepo string, tags []string, credentials map[string]string, pm *packageManager) string {
	var b strings.Builder
	b.WriteString("You are generating a Dockerfile to run tests for a software project in a CI environment.\n\n")
	fmt.Fprintf(&b, "Test command: %s\n\n", testCommand)

	if pm != nil {
		fmt.Fprintf(&b, "Package manager: %s (lockfile: %s)\nInstall command: %s\n\n",
			pm.name, pm.lockfile, pm.installCommand)
	}

	b.WriteString("Repository context:\n")
	b.WriteString(context)
	b.WriteString("\n\n")

	if existingDockerfiles != "" {
		b.WriteString("Existing Dockerfiles in this repo (use as reference for base images, build steps, and patterns):\n")
		b.WriteString(existingDockerfiles)
		b.WriteString("\n\n")
	}

	b.WriteString("Requirements:\n")
	if len(tags) > 0 {
		fmt.Fprintf(&b, "- Use %s as the base image. The following tags are currently available on Docker Hub — choose the most recent stable one by reasoning about the version numbers in the tag names:\n", baseImageRepo)
		for _, t := range tags {
			fmt.Fprintf(&b, "  - %s\n", t)
		}
		b.WriteString("  Avoid tags marked alpha, beta, rc, snapshot, edge, or that reference very old major versions.\n")
	} else {
		b.WriteString("- Use an appropriate official base image from Docker Hub for the detected language and tooling.\n")
		b.WriteString("  Pin a specific version tag — do not use \"latest\" — but aim for the most current stable release available.\n")
	}

	b.WriteString("- Install any additional system-level dependencies needed to run the test command.\n")

	if pm != nil {
		fmt.Fprintf(&b, "- Use `%s` to install dependencies (not npm ci or npm install unless the project uses npm).\n", pm.installCommand)
		if pm.name == "pnpm" {
			b.WriteString("- Install pnpm first: `RUN corepack enable && corepack prepare pnpm --activate` (this respects the packageManager version in package.json; do NOT pin pnpm@latest).\n")
		}
	}

	b.WriteString(`- CRITICAL: Use a single ` + "`COPY . .`" + ` to copy the entire repository. The build context already contains exactly the git-tracked files. Do NOT use selective COPY commands (e.g. COPY package*.json) — they will break in monorepos and non-standard layouts.
- After COPY, initialize a git repository so that tests relying on git work:
  RUN git init && git remote add origin https://github.com/placeholder/repo.git && git add -A && git commit -m "init" --allow-empty
  This is required because the build context does not include the .git directory.
`)

	if len(credentials) > 0 {
		b.WriteString("The following credentials have been collected and will be passed as Docker build args:\n")
		argNames := make([]string, 0, len(credentials))
		for k := range credentials {
			argNames = append(argNames, k)
			fmt.Fprintf(&b, "  - %s\n", k)
		}
		fmt.Fprintf(&b, "Use ARG %s in the Dockerfile to receive them, and use them to authenticate private dependencies.\n\n", strings.Join(argNames, " \\\n    ARG "))
	}

	b.WriteString("- Do NOT include the test command itself in the Dockerfile.\n")
	b.WriteString("- Output ONLY valid Dockerfile content. No markdown, no explanation, no code fences.")
	return b.String()
}

func dockerfileFixPrompt(dockerfile, buildError, testCommand, context string, pm *packageManager) string {
	var b strings.Builder
	b.WriteString("The following Dockerfile failed when building or running tests.\n\n")
	fmt.Fprintf(&b, "Test command: %s\n\n", testCommand)

	if pm != nil {
		fmt.Fprintf(&b, "Package manager: %s (lockfile: %s)\nInstall command: %s\n\n",
			pm.name, pm.lockfile, pm.installCommand)
	}

	b.WriteString("Repository context:\n")
	b.WriteString(context)
	b.WriteString("\n\n")

	b.WriteString("Current Dockerfile:\n")
	b.WriteString(dockerfile)
	b.WriteString("\n\n")

	// Truncate error output to 3000 chars
	errOutput := buildError
	if len(errOutput) > 3000 {
		errOutput = errOutput[:3000]
	}
	b.WriteString("Error output:\n")
	b.WriteString(errOutput)
	b.WriteString("\n\n")

	b.WriteString(`Fix the Dockerfile to resolve the error.
CRITICAL: Use a single ` + "`COPY . .`" + ` to copy the entire repository — do NOT use selective COPY commands like ` + "`COPY package*.json`" + `.
After COPY, ensure a git repository is initialized: RUN git init && git remote add origin https://github.com/placeholder/repo.git && git add -A && git commit -m "init" --allow-empty
Output ONLY valid Dockerfile content. No markdown, no explanation, no code fences.`)
	return b.String()
}
