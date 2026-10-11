package sandboximage

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestCopySources(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		want       []string
		wantErr    string
	}{
		{"flags, multiple sources, dedupe", "FROM x\nCOPY --chown=u a.json b.json /tmp/\ncopy a.json /x/\n", []string{"a.json", "b.json"}, ""},
		{"continuation and comments", "FROM x\n# COPY nope /x\nCOPY a \\\n  b /d/\n", []string{"a", "b"}, ""},
		{"cleaned", "COPY ./a/../b.txt /d/\n", []string{"b.txt"}, ""},
		{"from another stage is skipped", "COPY --from=builder /out /app\n", nil, ""},
		{"parent escape", "COPY ../secret /x\n", nil, "outside the repo"},
		{"absolute", "COPY /etc/passwd /x\n", nil, "inside the repo"},
		{"url", "COPY https://example.com/x /x\n", nil, "inside the repo"},
		{"wildcard", "COPY *.json /x\n", nil, "wildcards"},
		{"run lines ignored", "RUN echo COPY a b\n", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CopySources(tt.dockerfile)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		assert.NilError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		assert.NilError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return dir
}

func TestContextTagAndTar(t *testing.T) {
	files := map[string]string{
		"Dockerfile.smelt": "FROM x\nCOPY go.mod go.sum /tmp/\n",
		"go.mod":           "module a\n",
		"go.sum":           "sum\n",
		"main.go":          "package main\n",
	}
	dir := writeRepo(t, files)
	c, err := ReadContext(dir, "Dockerfile.smelt")
	assert.NilError(t, err)
	tag, err := c.Tag("repo")
	assert.NilError(t, err)
	assert.Equal(t, len(tag), len("smelt-repo:")+12)

	// Unrelated files do not change the tag; a copied file does.
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("changed"), 0o644))
	again, err := c.Tag("repo")
	assert.NilError(t, err)
	assert.Equal(t, again, tag)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.sum"), []byte("new"), 0o644))
	changed, err := c.Tag("repo")
	assert.NilError(t, err)
	assert.Assert(t, changed != tag)

	var buf bytes.Buffer
	assert.NilError(t, c.Tar(&buf))
	var names []string
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		assert.NilError(t, err)
		names = append(names, h.Name)
	}
	assert.DeepEqual(t, names, []string{"Dockerfile.smelt", "go.mod", "go.sum"})
}

func TestSlug(t *testing.T) {
	assert.Equal(t, Slug("React App!"), "react-app")
	assert.Equal(t, Slug("circleci-demo-javascript-react-app"), "circleci-demo-javascript-react-app")
	assert.Equal(t, Slug("###"), "repo")
}
