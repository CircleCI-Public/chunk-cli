package server

import (
	"sync/atomic"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/circleci"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/smeltd"
)

// The daemon builds its CircleCI client only for work that is CircleCI's. A
// token on the machine is not consent, so nothing else may resolve one.
func TestDaemonResolvesTheCircleCIClientOnlyForCircleCIProjects(t *testing.T) {
	var resolved atomic.Int32
	newDaemon := func() *daemon {
		d := newTestDaemon()
		d.cci.resolve = func() (*circleci.Client, string) {
			resolved.Add(1)
			return nil, "no credentials in this test"
		}
		return d
	}

	t.Run("nothing that is not CircleCI's resolves it", func(t *testing.T) {
		resolved.Store(0)
		d := newDaemon()

		// What every dashboard snapshot asks.
		assert.Equal(t, d.factoryAuthError("", harness.Claude), "no claude credential configured for the smelt daemon — factory runs unavailable (run: smelt auth login claude)")
		_ = d.factoryAuthError(smeltd.BackendDocker, harness.Claude)
		assert.Assert(t, d.factoryClient("") == nil)
		assert.Assert(t, d.factoryClient(smeltd.BackendDocker) == nil)

		for name, config := range map[string]string{
			"no config":       "",
			"default backend": `{"orgID":"org-1"}`,
			"docker backend":  `{"orgID":"org-1","factory":{"backend":"docker"}}`,
		} {
			root := t.TempDir()
			if config != "" {
				writeProjectConfig(t, root, config)
			}
			assert.Assert(t, d.validateProvisioner(smeltd.ValidateRequest{OrgID: "org-1", ProjectRoot: root}) == nil, name)
		}
		assert.Equal(t, resolved.Load(), int32(0))
	})

	t.Run("a circleci project resolves it, once", func(t *testing.T) {
		resolved.Store(0)
		d := newDaemon()
		root := t.TempDir()
		writeProjectConfig(t, root, `{"orgID":"org-1","factory":{"backend":"circleci"}}`)

		for range 3 {
			_ = d.validateProvisioner(smeltd.ValidateRequest{OrgID: "org-1", ProjectRoot: root})
		}
		assert.Equal(t, resolved.Load(), int32(1))
		assert.Equal(t, d.cci.authMessage(), "no credentials in this test")
	})

	t.Run("a factory run on the circleci backend resolves it", func(t *testing.T) {
		resolved.Store(0)
		d := newDaemon()
		d.rcfg.Credential.Value = "cred"

		assert.Check(t, d.factoryAuthError(smeltd.BackendCircleCI, harness.Claude) != "")
		assert.Equal(t, resolved.Load(), int32(1))
	})
}
