package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestInUseIDsReadsEveryKindOfLocalState(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv(config.EnvXDGDataHome, dataHome)
	root := filepath.Join(dataHome, "chunk")
	files := map[string]string{
		"proj/sidecar.json":                   `{"sidecar_ids":["active"]}`,
		"proj/pools/validate.json":            `{"sidecar_ids":["pool-a","pool-b"]}`,
		"proj/review-run.json":                `{"sidecar_id":"review"}`,
		"repo-x/factory/20261015-1.pool.json": `{"sidecar_ids":["factory"]}`,
		"proj/snapshot.json":                  `{"id":"a-snapshot"}`,
		"proj/validate-cache/k.json":          `{"sidecar_id":"cached"}`,
		"proj/broken.json":                    `{`,
		"repo-x/factory/20261015-1/.git":      "gitdir: elsewhere\n",
		"repo-x/factory/20261015-1/t/s.json":  `{"sidecar_id":"in-a-checkout"}`,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		assert.NilError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		assert.NilError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	ids, err := InUseIDs()

	assert.NilError(t, err)
	assert.DeepEqual(t, ids, map[string]bool{
		"active": true, "pool-a": true, "pool-b": true, "review": true, "factory": true,
	})
}

func TestInUseIDsWithNoStateIsEmpty(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	ids, err := InUseIDs()

	assert.NilError(t, err)
	assert.Equal(t, len(ids), 0)
}
