package status

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/sandboximage"
)

// DefaultProbeTimeout bounds running an unlabeled image to see which agents it has.
const DefaultProbeTimeout = 20 * time.Second

// inspectTimeout bounds the lookup of the image, so an absent daemon is not a hang.
const inspectTimeout = 5 * time.Second

// imagesFile caches, per image ID, the agents found by running the image.
const imagesFile = "images.json"

// ImageSource looks at the sandbox image on the Docker host.
type ImageSource interface {
	// InspectImage reports whether tag is on the host and what its labels say.
	InspectImage(ctx context.Context, tag string) (sandboximage.ImageInfo, error)
	// RunProbe runs the image once, without network, and lists the agent CLIs
	// found in it.
	RunProbe(ctx context.Context, tag string) ([]string, error)
}

// imageCheck is the answer about the image: what to report and the note that
// goes with it.
type imageCheck struct {
	report *ImageReport
	note   string
}

// checkImage finds out which agents the sandbox image carries. A label is
// believed; an image without one is run once, and the answer kept per image ID
// so the next call starts no container.
func (d Deps) checkImage(ctx context.Context) imageCheck {
	if d.Images == nil || d.ImageTag == "" {
		return imageCheck{}
	}
	inspectCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	info, err := d.Images.InspectImage(inspectCtx, d.ImageTag)
	rep := &ImageReport{Tag: d.ImageTag, ID: info.ID, Status: info.Status, Kit: info.Kit}
	switch {
	case err != nil:
		return imageCheck{report: rep, note: "could not look up the sandbox image: " + err.Error()}
	case info.Status == sandboximage.ImageMissing:
		return imageCheck{report: rep, note: fmt.Sprintf("sandbox image %s is not built; run smelt factory image", d.ImageTag)}
	case info.Status == sandboximage.ImageLabeled:
		rep.Found = info.Harnesses
		return imageCheck{report: rep}
	}
	return d.probeUnlabeled(ctx, rep)
}

// updateKitHint says how to get an image with a label.
const updateKitHint = "update the Dockerfile's agent kit with: smelt factory image --update-kit"

func (d Deps) probeUnlabeled(ctx context.Context, rep *ImageReport) imageCheck {
	const lead = "image has no " + sandboximage.LabelHarnesses + " label (agent kit v1)"
	cache := ""
	if d.Dir != "" {
		cache = filepath.Join(d.Dir, imagesFile)
	}
	found, cached := readImageCache(cache, rep.ID)
	if !cached {
		timeout := cmp.Or(d.ProbeTimeout, DefaultProbeTimeout)
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var err error
		if found, err = d.Images.RunProbe(ctx, d.ImageTag); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("timed out after %s", timeout)
			}
			return imageCheck{report: rep, note: fmt.Sprintf("%s; checking it by running it failed: %v; %s", lead, err, updateKitHint)}
		}
		writeImageCache(cache, rep.ID, found)
	}
	rep.Found, rep.Probed = found, true
	return imageCheck{report: rep, note: lead + "; checked by running it; " + updateKitHint}
}

func readImageCache(path, id string) ([]string, bool) {
	if id == "" || path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var m map[string][]string
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	found, ok := m[id]
	return found, ok
}

// writeImageCache keeps found for the image id. It is best effort: a cache
// that cannot be written only costs another container start.
func writeImageCache(path, id string, found []string) {
	if id == "" || path == "" {
		return
	}
	var m map[string][]string
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string][]string{}
	}
	m[id] = slices.Clone(found)
	if m[id] == nil {
		m[id] = []string{}
	}
	raw, err := json.Marshal(m)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}

// Describe is the image line of the human output, e.g.
// "smelt-x:f81c (kit v1, unlabeled; claude found, codex not)".
func (r ImageReport) Describe(names []string) string {
	switch r.Status {
	case sandboximage.ImageMissing:
		return r.Tag + " (not built)"
	case sandboximage.ImageLabeled, sandboximage.ImageUnlabeled:
		return fmt.Sprintf("%s (kit v%d, %s; %s)", r.Tag, r.Kit, r.Status, r.foundText(names))
	}
	return r.Tag
}

func (r ImageReport) foundText(names []string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if slices.Contains(r.Found, n) {
			parts = append(parts, n+" found")
		} else {
			parts = append(parts, n+" not")
		}
	}
	return strings.Join(parts, ", ")
}
