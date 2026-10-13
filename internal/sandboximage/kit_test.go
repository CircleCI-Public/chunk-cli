package sandboximage

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/smeltdev/smelt/internal/envspec"
)

func TestKitVersionOf(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		version   int
		found     bool
		outdated  bool
		wantNewer bool
	}{
		{"no marker", "FROM x\nRUN true\n", 1, false, true, false},
		{"v1 marker without number", "FROM x\n# --- smelt agent kit ------\nRUN true\n", 1, true, true, false},
		{"v2", "FROM x\n# --- smelt agent kit (v2) ------\n", 2, true, true, false},
		{"current", "FROM x\n# --- smelt agent kit (v3) ------\n", 3, true, false, false},
		{"newer than the binary", "FROM x\n# --- smelt agent kit (v4) ------\n", 4, true, false, true},
		{"garbage after the marker", "# --- smelt agent kit (vX) ---\n", 1, true, true, false},
		{"huge number", "# --- smelt agent kit (v99999999999999999999) ---\n", 1, true, true, false},
		{"marker in a comment elsewhere", "FROM x\n# see the '# --- smelt agent kit (v2)' section\nRUN echo # --- smelt agent kit (v2)\n", 1, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, found := KitVersionOf(tt.in)
			assert.Equal(t, v, tt.version)
			assert.Equal(t, found, tt.found)
			assert.Equal(t, KitOutdated(tt.in), tt.outdated)
			assert.Equal(t, v > KitVersion, tt.wantNewer)
		})
	}
}

func TestRenderedKitIsCurrent(t *testing.T) {
	got := Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	v, found := KitVersionOf(got)
	assert.Check(t, found)
	assert.Check(t, cmp.Equal(v, KitVersion))
	assert.Check(t, cmp.Contains(got, "LABEL "+LabelHarnesses+"=claude,codex "+LabelKit+"=3"))
}

func TestUpdateKitReplacesOnlyTheKitSection(t *testing.T) {
	current := Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	head, _, _ := strings.Cut(current, "\n# --- smelt agent kit")
	old := head + "\nRUN my-hand-edited-toolchain\n\n# --- smelt agent kit ------\nRUN curl claude\nCMD [\"sleep\", \"infinity\"]\n"

	got, err := UpdateKit(old)
	assert.NilError(t, err)
	assert.Check(t, strings.HasPrefix(got, head+"\nRUN my-hand-edited-toolchain\n\n"))
	assert.Check(t, cmp.Contains(got, KitCodex()))
	assert.Check(t, !KitOutdated(got))
	assert.Check(t, cmp.Equal(strings.Count(got, kitMarkerPrefix), 1))

	again, err := UpdateKit(got)
	assert.NilError(t, err)
	assert.Equal(t, again, got)
}

func TestUpdateKitKeepsTheBaseUsersSwitch(t *testing.T) {
	r := NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil)
	r.User = "circleci"
	current := Render(r)
	old, _, _ := strings.Cut(current, "\n# --- smelt agent kit")
	old += "\n# --- smelt agent kit ---\nUSER root\nRUN apt-get install git\nUSER circleci\nRUN curl claude\n"
	got, err := UpdateKit(old)
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(got, "USER root\nRUN apt-get update"))
	assert.Check(t, strings.HasSuffix(strings.TrimSuffix(got, "CMD [\"sleep\", \"infinity\"]\n"), "LABEL "+LabelHarnesses+"=claude,codex "+LabelKit+"=3\n"))
	assert.Check(t, cmp.Contains(got, "USER circleci\nRUN curl"))
}

// A v2 Dockerfile, plain or with the extra RUN block that was patched in by
// hand to download the command host, comes out as exactly the v3 kit.
func TestUpdateKitBringsV2UpToV3(t *testing.T) {
	current := Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	head, kit, _ := strings.Cut(current, "\n# --- smelt agent kit")
	v2 := "\n# --- smelt agent kit (v2) ---\n" + strings.Replace(strings.SplitN(kit, "\n", 2)[1], codexHost.install(), "", 1)
	v2 = strings.ReplaceAll(v2, "="+kitVersionString()+"\n", "=2\n")
	patched := strings.Replace(v2, "LABEL ", "RUN curl -fsSL -o /tmp/h.tgz https://example.invalid/codex-code-mode-host.tar.gz\nLABEL ", 1)

	for name, in := range map[string]string{"plain": head + v2, "hand patched": head + patched} {
		t.Run(name, func(t *testing.T) {
			assert.Check(t, KitOutdated(in))
			assert.Check(t, cmp.Contains(KitOutdatedWarning("Dockerfile.smelt", 2), "--update-kit"))
			got, err := UpdateKit(in)
			assert.NilError(t, err)
			assert.Equal(t, got, current)
			assert.Check(t, cmp.Equal(strings.Count(got, "codex-code-mode-host-${arch}"), 1))
		})
	}
}

func TestUpdateKitWithoutAMarker(t *testing.T) {
	_, err := UpdateKit("FROM x\nRUN true\n")
	assert.ErrorIs(t, err, ErrNoKit)
}
