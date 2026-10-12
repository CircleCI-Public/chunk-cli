package sandboximage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"

	"github.com/smeltdev/smelt/envbuilder"
	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/ciconfig"
	"github.com/smeltdev/smelt/internal/envspec"
	"github.com/smeltdev/smelt/internal/gitexec"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar/docker"
)

// Generated is a rendered Dockerfile and the Recipe it came from.
type Generated struct {
	Recipe     Recipe
	Dockerfile string
}

// Generate detects dir's stack, reads its CI config and tracked files, and
// renders a Dockerfile for it, returning the Recipe too so the caller can say
// where the base image came from. env, when non-nil, is a
// detection result the caller already has.
func (b *Builder) Generate(ctx context.Context, dir string, env *envspec.Environment) (Generated, error) {
	if env == nil {
		detected, err := envbuilder.DetectEnvironment(ctx, dir)
		if err != nil {
			return Generated{}, fmt.Errorf("detect environment: %w", err)
		}
		env = detected
	}
	// A repo with no CI config, or one that cannot be read, simply has no CI
	// image to prefer.
	ci, err := ciconfig.Extract(dir)
	if err != nil && !errors.Is(err, ciconfig.ErrNotFound) {
		var cfgErr *ciconfig.ConfigError
		if !errors.As(err, &cfgErr) {
			return Generated{}, err
		}
		ci = nil
	}
	tracked, err := trackedFiles(ctx, dir)
	if err != nil {
		return Generated{}, err
	}
	r := NewRecipe(env, ci, tracked)
	user, err := b.imageUser(ctx, r.Base)
	if err != nil {
		return Generated{}, err
	}
	r.User = user
	return Generated{Recipe: r, Dockerfile: Render(r)}, nil
}

// trackedFiles lists the files git tracks in dir, slash-separated.
func trackedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := gitexec.Runner{Dir: dir}.Output(ctx, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	var files []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// imageUser is the user the base image runs as, empty for root. The image is
// pulled when it is not on the host, since its config is the only place the
// user is recorded.
func (b *Builder) imageUser(ctx context.Context, ref string) (string, error) {
	info, err := b.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		rc, pullErr := b.cli.ImagePull(ctx, ref, image.PullOptions{})
		if pullErr != nil {
			return "", fmt.Errorf("pull base image %s: %w", ref, pullErr)
		}
		_, copyErr := io.Copy(io.Discard, rc)
		_ = rc.Close()
		if copyErr != nil {
			return "", fmt.Errorf("pull base image %s: %w", ref, copyErr)
		}
		info, err = b.cli.ImageInspect(ctx, ref)
	}
	if err != nil {
		return "", fmt.Errorf("inspect base image %s: %w", ref, err)
	}
	user, _, _ := strings.Cut(info.Config.User, ":")
	if user == rootUser || user == "0" {
		return "", nil
	}
	return user, nil
}

// Plan is a Dockerfile resolved to the image it builds.
type Plan struct {
	Context *Context
	Slug    string
	Tag     string
}

// NewPlan reads dockerfile (repo-relative) in dir and computes its image tag.
func NewPlan(dir, dockerfile string) (Plan, error) {
	c, err := ReadContext(dir, dockerfile)
	if err != nil {
		return Plan{}, err
	}
	slug := Slug(filepath.Base(dir))
	tag, err := c.Tag(slug)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Context: c, Slug: slug, Tag: tag}, nil
}

// EnsureOptions steer Ensure.
type EnsureOptions struct {
	// Rebuild builds even when the tag exists, without the layer cache.
	Rebuild  bool
	LogPath  string
	Progress iostream.StatusFunc
	// Building is called before a build starts, so the caller can say why.
	Building func(tag string)
}

// Ensure builds the plan's image when it is missing (or Rebuild is set) and
// reports whether it built.
func (b *Builder) Ensure(ctx context.Context, p Plan, opts EnsureOptions) (bool, error) {
	if !opts.Rebuild {
		exists, err := b.Exists(ctx, p.Tag)
		if err != nil || exists {
			return false, err
		}
	}
	if opts.Building != nil {
		opts.Building(p.Tag)
	}
	return true, b.Build(ctx, BuildOptions{
		Context: p.Context, Slug: p.Slug, Tag: p.Tag,
		NoCache: opts.Rebuild, LogPath: opts.LogPath, Progress: opts.Progress,
	})
}

// WriteDockerfile writes content to dockerfile in dir, refusing to replace a
// file that is already there.
func WriteDockerfile(dir, dockerfile, content string) error {
	f, err := os.OpenFile(filepath.Join(dir, dockerfile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", dockerfile, err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", dockerfile, err)
	}
	return f.Close()
}

// Connect returns a Builder on the local Docker daemon. Close releases it.
func Connect() (*Builder, error) {
	cli, err := docker.NewClient()
	if err != nil {
		return nil, err
	}
	return &Builder{cli: cli, closer: cli}, nil
}

// Close releases the Docker client, if the Builder owns one.
func (b *Builder) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

// Diff returns a unified diff from the Dockerfile on disk to next, empty when
// they are the same.
func Diff(ctx context.Context, dir, dockerfile, next string) (string, error) {
	current, err := os.ReadFile(filepath.Join(dir, dockerfile))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dockerfile, err)
	}
	return DiffText(ctx, dockerfile, string(current), next)
}

// DiffText returns a unified diff between two versions of the file called name.
func DiffText(ctx context.Context, name, before, after string) (string, error) {
	tmp, err := os.MkdirTemp("", brand.Command+"-diff-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for dir, content := range map[string]string{"a": before, "b": after} {
		if err := os.MkdirAll(filepath.Join(tmp, dir), 0o755); err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(tmp, dir, name), []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write temp file: %w", err)
		}
	}
	// git diff --no-index exits 1 when the files differ.
	out, err := gitexec.Runner{Dir: tmp}.Output(ctx, "diff", "--no-index", "--no-color", "--", "a/"+name, "b/"+name)
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", fmt.Errorf("diff %s: %w", name, err)
	}
	return string(out), nil
}
