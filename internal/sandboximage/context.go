package sandboximage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/hashutil"
)

// tagHexLen is how much of the content digest goes into a tag.
const tagHexLen = 12

// CopySources returns the repo-relative files the Dockerfile COPYs from the
// build context, in order and without duplicates. It reads the Dockerfile, so
// it works on one that chunk did not generate. A source that is absolute,
// escapes the context, uses a wildcard, or comes from another stage or URL is
// an error: chunk sends exactly these files, so it must know them.
func CopySources(dockerfile string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, line := range logicalLines(dockerfile) {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "COPY") {
			continue
		}
		var args []string
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "--") {
				if strings.HasPrefix(f, "--from") {
					args = nil
					break
				}
				continue
			}
			args = append(args, f)
		}
		if len(args) < 2 {
			continue // a copy between stages, or one with no sources
		}
		for _, src := range args[:len(args)-1] {
			clean, err := cleanSource(src)
			if err != nil {
				return nil, err
			}
			if !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
	}
	return out, nil
}

// logicalLines joins backslash continuations and drops comments.
func logicalLines(dockerfile string) []string {
	var out []string
	var cur strings.Builder
	sc := bufio.NewScanner(strings.NewReader(dockerfile))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if before, ok := strings.CutSuffix(line, "\\"); ok {
			cur.WriteString(before + " ")
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var wildcard = regexp.MustCompile(`[*?\[]`)

func cleanSource(src string) (string, error) {
	switch {
	case path.IsAbs(src) || strings.Contains(src, "://"):
		return "", fmt.Errorf("COPY source %q must be a path inside the repo", src)
	case wildcard.MatchString(src):
		return "", fmt.Errorf("COPY source %q: wildcards are not supported; list the files", src)
	}
	clean := path.Clean(src)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("COPY source %q is outside the repo", src)
	}
	return clean, nil
}

// Context is the build context for a Dockerfile: the Dockerfile itself and the
// files it COPYs, read from a repo directory.
type Context struct {
	Dockerfile string // repo-relative path
	Content    string // the Dockerfile's text
	Sources    []string
	dir        string
}

// ReadContext reads dockerfile (repo-relative) and resolves its COPY sources.
func ReadContext(dir, dockerfile string) (*Context, error) {
	content, err := os.ReadFile(filepath.Join(dir, dockerfile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dockerfile, err)
	}
	sources, err := CopySources(string(content))
	if err != nil {
		return nil, err
	}
	return &Context{Dockerfile: dockerfile, Content: string(content), Sources: sources, dir: dir}, nil
}

// walk calls fn for every regular file a source names, expanding directories,
// with the file's slash-separated repo-relative path.
func (c *Context) walk(fn func(rel string, info fs.FileInfo) error) error {
	for _, src := range c.Sources {
		root := filepath.Join(c.dir, filepath.FromSlash(src))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(c.dir, p)
			if err != nil {
				return err
			}
			return fn(filepath.ToSlash(rel), info)
		})
		if err != nil {
			return fmt.Errorf("COPY source %s: %w", src, err)
		}
	}
	return nil
}

// Tar writes the context as a tar stream: the Dockerfile and the COPY sources,
// at their repo-relative paths.
func (c *Context) Tar(w io.Writer) error {
	tw := tar.NewWriter(w)
	if err := writeTarFile(tw, c.Dockerfile, []byte(c.Content), 0o644); err != nil {
		return err
	}
	err := c.walk(func(rel string, info fs.FileInfo) error {
		if rel == c.Dockerfile {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(c.dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		return writeTarFile(tw, rel, data, int64(info.Mode().Perm()))
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
		return fmt.Errorf("tar %s: %w", name, err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("tar %s: %w", name, err)
	}
	return nil
}

// Tag names the image for this context: the repo slug and a digest of the
// Dockerfile plus each COPY source's path and content. The image is stale
// exactly when its tag does not exist.
func (c *Context) Tag(slug string) (string, error) {
	parts := []string{c.Content}
	err := c.walk(func(rel string, _ fs.FileInfo) error {
		data, err := os.ReadFile(filepath.Join(c.dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		parts = append(parts, rel, string(data))
		return nil
	})
	if err != nil {
		return "", err
	}
	return ImageName(slug) + ":" + hashutil.SumParts(parts...)[:tagHexLen], nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9_.-]+`)

// Slug turns a repo directory name into something usable in an image name.
func Slug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-._")
	if s == "" {
		return "repo"
	}
	return s
}

// ImageName is the repository part of a sandbox image tag.
func ImageName(slug string) string { return "chunk-" + slug }
