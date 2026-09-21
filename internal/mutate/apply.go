package mutate

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
)

// MutationPatch computes a unified diff for mutation m without modifying any
// local file. The patch is suitable for applying on a synced sidecar workspace.
func MutationPatch(workDir string, m Mutation) ([]byte, error) {
	filename := filepath.Join(workDir, m.File)
	orig, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", m.File, err)
	}
	mutated, err := mutateSrc(filename, orig, m)
	if err != nil {
		return nil, err
	}
	return computePatch(m.File, orig, mutated)
}

// mutateSrc parses src, applies the token substitution described by m, and
// returns the reprinted source without modifying the file on disk.
func mutateSrc(filename string, src []byte, m Mutation) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.File, err)
	}

	applied := false
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil || applied {
			return false
		}
		tok, pos, ok := operatorTokenAt(n)
		if !ok {
			return true
		}
		p := fset.Position(pos)
		if p.Line != m.Line || p.Column != m.Col {
			return true
		}
		newTok, ok := mutatedToken(m.Operator, *tok)
		if !ok {
			return true
		}
		*tok = newTok
		applied = true
		return false
	})

	if !applied {
		return nil, fmt.Errorf("mutation site not found: %s:%d:%d (%s)", m.File, m.Line, m.Col, m.Operator)
	}

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, f); err != nil {
		return nil, fmt.Errorf("print %s: %w", m.File, err)
	}
	return buf.Bytes(), nil
}

// computePatch generates a unified diff between orig and mutated for file.
// diff exits with status 1 when differences are found, which is the success
// case for mutation patches.
func computePatch(file string, orig, mutated []byte) ([]byte, error) {
	tmpA, err := os.CreateTemp("", "chunk-orig-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	nameA := tmpA.Name()
	defer func() { _ = os.Remove(nameA) }()
	if _, err := tmpA.Write(orig); err != nil {
		_ = tmpA.Close()
		return nil, err
	}
	if err := tmpA.Close(); err != nil {
		return nil, err
	}

	tmpB, err := os.CreateTemp("", "chunk-mut-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	nameB := tmpB.Name()
	defer func() { _ = os.Remove(nameB) }()
	if _, err := tmpB.Write(mutated); err != nil {
		_ = tmpB.Close()
		return nil, err
	}
	if err := tmpB.Close(); err != nil {
		return nil, err
	}

	out, err := exec.Command("diff", "-u", "--label", "a/"+file, "--label", "b/"+file, nameA, nameB).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return out, nil
		}
		return nil, fmt.Errorf("diff: %w", err)
	}
	return out, nil
}

// operatorTokenAt extracts the mutable token pointer and its position from a node.
func operatorTokenAt(n ast.Node) (tok *token.Token, pos token.Pos, ok bool) {
	switch n := n.(type) {
	case *ast.BinaryExpr:
		return &n.Op, n.OpPos, true
	case *ast.UnaryExpr:
		return &n.Op, n.OpPos, true
	case *ast.AssignStmt:
		return &n.Tok, n.TokPos, true
	case *ast.IncDecStmt:
		return &n.Tok, n.TokPos, true
	}
	return nil, 0, false
}
