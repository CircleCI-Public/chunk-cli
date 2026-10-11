package sandboximage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/iostream"
)

// LabelSandbox marks built images with the repo slug, so a later clean can
// find and prune old tags.
const LabelSandbox = brand.LabelPrefix + "sandbox"

// logTailLines is how much of a failed build's log is kept for the error.
const logTailLines = 150

// dockerAPI is the part of the Docker client a Builder uses.
type dockerAPI interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, options build.ImageBuildOptions) (build.ImageBuildResponse, error)
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// Builder builds sandbox images on the local Docker daemon.
type Builder struct {
	cli    dockerAPI
	closer io.Closer
}

// NewBuilder returns a Builder around a Docker client.
func NewBuilder(cli dockerAPI) *Builder { return &Builder{cli: cli} }

// BuildOptions says what to build.
type BuildOptions struct {
	Context *Context
	Slug    string
	Tag     string
	// NoCache rebuilds every layer.
	NoCache bool
	// LogPath receives the full build log; empty keeps none.
	LogPath string
	// Progress receives each build step line.
	Progress iostream.StatusFunc
}

// BuildError is a failed build, carrying the end of its log for the repair
// loop and the error message.
type BuildError struct {
	Tag     string
	LogTail string
	LogPath string
	Err     error
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("build %s: %v", e.Tag, e.Err)
}

func (e *BuildError) Unwrap() error { return e.Err }

// Exists reports whether tag is on the Docker host.
func (b *Builder) Exists(ctx context.Context, tag string) (bool, error) {
	_, err := b.cli.ImageInspect(ctx, tag)
	switch {
	case err == nil:
		return true, nil
	case cerrdefs.IsNotFound(err):
		return false, nil
	}
	return false, fmt.Errorf("look up image %s: %w", tag, err)
}

// Build builds the image. A failure to build is a *BuildError.
func (b *Builder) Build(ctx context.Context, opts BuildOptions) error {
	var tarball bytes.Buffer
	if err := opts.Context.Tar(&tarball); err != nil {
		return fmt.Errorf("prepare build context: %w", err)
	}
	resp, err := b.cli.ImageBuild(ctx, &tarball, build.ImageBuildOptions{
		Tags:       []string{opts.Tag},
		Dockerfile: opts.Context.Dockerfile,
		Labels:     map[string]string{LabelSandbox: opts.Slug},
		NoCache:    opts.NoCache,
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("build %s: %w", opts.Tag, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var logFile *os.File
	if opts.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o755); err != nil {
			return fmt.Errorf("create build log dir: %w", err)
		}
		logFile, err = os.Create(opts.LogPath)
		if err != nil {
			return fmt.Errorf("create build log: %w", err)
		}
		defer func() { _ = logFile.Close() }()
	}

	var lines tail
	buildErr := readBuildStream(resp.Body, func(line string) {
		lines.add(line)
		if logFile != nil {
			_, _ = io.WriteString(logFile, line+"\n")
		}
		if opts.Progress != nil && strings.HasPrefix(line, "Step ") {
			opts.Progress(iostream.LevelInfo, line)
		}
	})
	if buildErr != nil {
		return &BuildError{Tag: opts.Tag, LogTail: lines.String(), LogPath: opts.LogPath, Err: buildErr}
	}
	return nil
}

// streamMessage is one JSON line of the daemon's build output.
type streamMessage struct {
	Stream string `json:"stream"`
	Error  string `json:"error"`
	Aux    struct {
		ID string `json:"ID"`
	} `json:"aux"`
}

// readBuildStream calls emit for each output line and returns the build's
// error, if the daemon reported one.
func readBuildStream(r io.Reader, emit func(string)) error {
	var failure error
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m streamMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			emit(sc.Text())
			continue
		}
		for line := range strings.SplitSeq(strings.TrimRight(m.Stream, "\n"), "\n") {
			if line != "" {
				emit(line)
			}
		}
		if m.Error != "" {
			emit(m.Error)
			failure = errors.New(m.Error)
		}
	}
	if err := sc.Err(); err != nil && failure == nil {
		failure = fmt.Errorf("read build output: %w", err)
	}
	return failure
}

// tail keeps the last logTailLines lines.
type tail struct{ lines []string }

func (t *tail) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > logTailLines {
		t.lines = t.lines[len(t.lines)-logTailLines:]
	}
}

func (t *tail) String() string { return strings.Join(t.lines, "\n") }
