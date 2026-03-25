package hook

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
)

// getChangedFiles returns files that have changed in the repo.
// When stagedOnly is true, uses git diff --cached.
// When fileExt is set, filters to files matching that extension.
func getChangedFiles(cwd string, stagedOnly bool, fileExt string) ([]string, error) {
	var cmd *exec.Cmd
	if stagedOnly {
		cmd = exec.Command("git", "diff", "--cached", "--name-only", "--diff-filter=ACMR")
	} else {
		cmd = exec.Command("sh", "-c",
			"git status --porcelain -uall | grep -vE '^D.|^.D' | cut -c4- | sed 's/.* -> //'")
	}
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, nil // git errors treated as no changes
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var files []string
	for _, line := range lines {
		f := strings.TrimSpace(line)
		if f == "" {
			continue
		}
		// Remove surrounding quotes from git output
		if len(f) >= 2 && f[0] == '"' && f[len(f)-1] == '"' {
			f = f[1 : len(f)-1]
		}
		files = append(files, f)
	}

	if fileExt != "" {
		ext := fileExt
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		var filtered []string
		for _, f := range files {
			if strings.HasSuffix(f, ext) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}

	return files, nil
}

// detectChanges checks whether there are relevant changes in the repo.
func detectChanges(cwd string, fileExt string, staged bool) (bool, error) {
	if fileExt != "" {
		files, err := getChangedFiles(cwd, staged, fileExt)
		if err != nil {
			return false, err
		}
		return len(files) > 0, nil
	}

	if staged {
		return hasStagedChanges(cwd)
	}
	return hasUncommittedChanges(cwd)
}

func hasUncommittedChanges(cwd string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain", "-uall")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func hasStagedChanges(cwd string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--stat")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// getHeadSHA returns the current HEAD commit SHA.
func getHeadSHA(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// computeFingerprint computes a SHA-256 fingerprint of HEAD + working tree diff.
func computeFingerprint(cwd string, staged bool, fileExt string) string {
	head := getHeadSHA(cwd)
	if head == "" {
		return ""
	}

	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	} else {
		args = append(args, "HEAD")
	}

	if fileExt != "" {
		ext := fileExt
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		args = append(args, "--", fmt.Sprintf("*%s", ext))
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	h := sha256.Sum256([]byte(head + "\n" + string(out)))
	return fmt.Sprintf("%x", h)
}

// getChangedPackages deduplicates parent directories from file paths.
func getChangedPackages(files []string) []string {
	dirs := map[string]bool{}
	for _, f := range files {
		parts := strings.Split(f, "/")
		if len(parts) <= 1 {
			dirs["./"] = true
		} else {
			dir := "./" + strings.Join(parts[:len(parts)-1], "/")
			dirs[dir] = true
		}
	}
	var result []string
	for d := range dirs {
		result = append(result, d)
	}
	return result
}

// substitutePlaceholders replaces {{CHANGED_FILES}} and {{CHANGED_PACKAGES}}.
func substitutePlaceholders(command string, files []string) string {
	result := command
	if strings.Contains(result, "{{CHANGED_FILES}}") {
		var quoted []string
		for _, f := range files {
			quoted = append(quoted, shellQuoteWrap(f))
		}
		result = strings.Replace(result, "{{CHANGED_FILES}}", strings.Join(quoted, " "), 1)
	}
	if strings.Contains(result, "{{CHANGED_PACKAGES}}") {
		pkgs := getChangedPackages(files)
		var quoted []string
		for _, p := range pkgs {
			quoted = append(quoted, shellQuoteWrap(p))
		}
		result = strings.Replace(result, "{{CHANGED_PACKAGES}}", strings.Join(quoted, " "), 1)
	}
	return result
}

// shellQuoteWrap wraps a string in single quotes for safe shell usage.
func shellQuoteWrap(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
