package factory

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/sidecar"
)

func TestSkippedTests(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{"none", "ok  \tgithub.com/x/y\t0.01s\n--- PASS: TestA (0.00s)\n", nil},
		{"go test -v", "=== RUN   TestA\n--- SKIP: TestA (0.00s)\n    a_test.go:9: needs docker\n--- PASS: TestB (0.00s)\n    --- SKIP: TestB/sub (0.00s)\n",
			[]string{"TestA", "TestB/sub"}},
		{"gotestsum", "=== SKIP: internal/x TestDocker (0.00s)\n    x_test.go:4: no docker\n\n=== SKIP: internal/y TestOther (0.12s)\n",
			[]string{"internal/x TestDocker", "internal/y TestOther"}},
		{"both formats list a test once", "--- SKIP: TestA (0.00s)\n=== SKIP: TestA (0.00s)\n", []string{"TestA"}},
		{"a log line is not a skip", "level=info msg=\"--- SKIP: nothing\"\nSKIP this\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, SkippedTests(tt.output), tt.want)
		})
	}
}

func TestOutcomeSkippedCollectsFromChecks(t *testing.T) {
	o := Outcome{Checks: []Check{{Name: "test", Skipped: []string{"TestA"}}, {Name: "lint"}, {Name: "e2e", Skipped: []string{"TestB"}}}}
	assert.DeepEqual(t, o.Skipped(), []string{"TestA", "TestB"})
	assert.Equal(t, len((Outcome{}).Skipped()), 0)
}

func TestSkippedTestsAreInTheNotes(t *testing.T) {
	n := Notes{Title: "T", What: "- w", Why: "- y", How: "- h", Skipped: []string{"TestA", "TestB"}}
	assert.Assert(t, contains(n.PRBody("r", Outcome{}, ""), "Skipped in the sandbox: TestA, TestB"))
	assert.Assert(t, contains(n.CommitMessage("r", Outcome{}, ""), "Skipped in the sandbox: TestA, TestB"))

	fb := fallbackNotes("Title", "timed out")
	fb.Skipped = []string{"TestA"}
	assert.Assert(t, contains(fb.PRBody("r", Outcome{}, ""), "Skipped in the sandbox: TestA"))
	assert.Assert(t, !contains(Notes{Title: "T", What: "w", Why: "y", How: "h"}.PRBody("r", Outcome{}, ""), "Skipped"))
}

func TestRunCommandListsSkippedTests(t *testing.T) {
	exec := localExec(t.TempDir())
	entry := &sidecar.PoolEntry{ID: "impl", RepoPath: t.TempDir()}
	c := runCommand(t.Context(), exec, entry, config.Command{Name: "test", Run: "printf -- '--- SKIP: TestDocker (0.00s)\\nok\\n'"})
	assert.Equal(t, c.Status, StatusPassed)
	assert.DeepEqual(t, c.Skipped, []string{"TestDocker"})
}
