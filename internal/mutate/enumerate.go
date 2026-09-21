package mutate

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

const patternAll = "./..."

// GoEnumerator walks Go source files and enumerates mutation candidates
// using the built-in operator set. No external tools required.
type GoEnumerator struct {
	// Paths to search: package dirs, files, or ./... patterns.
	// Defaults to the full workDir tree when empty.
	Paths []string
}

func (g *GoEnumerator) Enumerate(_ context.Context, dir string) ([]Mutation, error) {
	paths := g.Paths
	if len(paths) == 0 {
		paths = []string{patternAll}
	}

	seen := map[string]bool{}
	var goFiles []string
	for _, p := range paths {
		files, err := findGoFiles(dir, p)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !seen[f] {
				seen[f] = true
				goFiles = append(goFiles, f)
			}
		}
	}
	sort.Strings(goFiles)

	ops := sortedOperators()

	seq := 0
	var mutations []Mutation
	for _, absFile := range goFiles {
		rel, err := filepath.Rel(dir, absFile)
		if err != nil {
			rel = absFile
		}
		ms, err := enumerateFile(absFile, rel, ops, &seq)
		if err != nil {
			return nil, err
		}
		mutations = append(mutations, ms...)
	}
	return mutations, nil
}

func enumerateFile(absPath, relPath string, ops []string, seq *int) ([]Mutation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, absPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", relPath, err)
	}

	var mutations []Mutation
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		tok, pos, ok := operatorTokenAt(n)
		if !ok {
			return true
		}
		position := fset.Position(pos)
		for _, op := range ops {
			if newTok, has := operatorMutations[op][*tok]; has {
				*seq++
				mutations = append(mutations, Mutation{
					ID:       fmt.Sprintf("MUT-%03d", *seq),
					File:     relPath,
					Line:     position.Line,
					Col:      position.Column,
					Operator: op,
					Status:   "RUNNABLE",
					Before:   tok.String(),
					After:    newTok.String(),
				})
			}
		}
		return true
	})
	return mutations, nil
}

// findGoFiles returns absolute paths to non-test .go files matching pattern.
// Pattern may be a file, a package dir, or end with /... for recursive search.
func findGoFiles(workDir, pattern string) ([]string, error) {
	recursive := pattern == patternAll || strings.HasSuffix(pattern, "/...")
	base := strings.TrimSuffix(pattern, "/...")
	base = strings.TrimSuffix(base, "...")
	if base == "" || base == "." || base == "./" {
		base = workDir
	} else if !filepath.IsAbs(base) {
		base = filepath.Join(workDir, base)
	}

	var files []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if !recursive && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func sortedOperators() []string {
	ops := make([]string, 0, len(operatorMutations))
	for op := range operatorMutations {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}
