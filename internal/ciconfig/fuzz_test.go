package ciconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzExtract checks that no config can make Extract panic. The file is
// whatever the repo being initialised happens to contain, so a panic here
// would take down `chunk init` on a repo the user cannot easily change.
//
// Without -fuzz this still runs the seed corpus as a regression test.
func FuzzExtract(f *testing.F) {
	seeds := []string{
		"",
		"version: 2.1\n",
		"setup: true\n",
		"jobs:\n",
		"jobs: []\n",
		"jobs: null\n",
		"workflows:\n  main:\n",
		"workflows:\n  main:\n    jobs: []\n",
		"workflows: version\n",
		"jobs:\n  test:\n    steps:\n      - run:\n",
		"jobs:\n  test:\n    steps:\n      - run: {}\n",
		"jobs:\n  test:\n    steps: [[]]\n",
		"jobs:\n  test:\n    steps:\n      - when:\n",
		"jobs:\n  test:\n    steps:\n      - a/b:\n          steps: notalist\n",
		"commands:\n  a:\n    steps:\n      - a\njobs:\n  build:\n    steps:\n      - a\n",
		"jobs:\n  build:\n    parameters:\n      x:\n        default: [1]\n    steps:\n      - run: echo << parameters.x >>\n",
		"workflows:\n  w:\n    jobs:\n      - t:\n          filters:\n            branches:\n              only: /[/\n",
		"version: 2.1\nworkflows:\n  w:\n    jobs:\n      - {}\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, body string) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".circleci"), 0o755); err != nil {
			t.Skip()
		}
		if err := os.WriteFile(filepath.Join(dir, ".circleci", "config.yml"), []byte(body), 0o644); err != nil {
			t.Skip()
		}

		res, err := Extract(dir, Options{})
		if err != nil {
			return
		}
		// On success the result must be self-consistent: never nil, and never
		// reporting more candidates than the cap the caller relies on.
		if res == nil {
			t.Fatal("nil result with nil error")
		}
		if len(res.Candidates) > maxCandidates {
			t.Fatalf("candidates %d exceeds cap %d", len(res.Candidates), maxCandidates)
		}
		for _, c := range res.Candidates {
			if c.Command == "" {
				t.Fatal("emitted candidate with empty command")
			}
		}
	})
}
