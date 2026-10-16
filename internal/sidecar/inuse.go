package sidecar

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// InUseIDs returns the IDs of every sidecar that chunk's local state on this
// machine refers to: active sidecars, pools, factory runs and detached
// reviews. Sidecars used only from another machine are not known here, and
// the repository checkouts of factory runs are not read.
func InUseIDs() (map[string]bool, error) {
	root, err := config.AppData()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	r, err := os.OpenRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	err = fs.WalkDir(r.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "validate-cache" {
				return fs.SkipDir
			}
			// A factory run's checkout is a whole repository; its state sits
			// beside it, and any JSON inside is the project's, not chunk's.
			if _, err := r.Stat(filepath.Join(path, ".git")); err == nil {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		data, err := r.ReadFile(path)
		if err != nil {
			return nil
		}
		var refs struct {
			SidecarIDs []string `json:"sidecar_ids"`
			SidecarID  string   `json:"sidecar_id"`
		}
		if json.Unmarshal(data, &refs) != nil {
			return nil
		}
		for _, id := range append(refs.SidecarIDs, refs.SidecarID) {
			if id != "" {
				ids[id] = true
			}
		}
		return nil
	})
	return ids, err
}
