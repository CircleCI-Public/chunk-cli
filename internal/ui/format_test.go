package ui

import (
	"strings"
	"testing"
)

func init() {
	SetColorEnabled(false)
}

func TestSuccess(t *testing.T) {
	got := Success("done")
	if got != "✓ done" {
		t.Errorf("Success(\"done\") = %q, want %q", got, "✓ done")
	}
}

func TestWarning(t *testing.T) {
	got := Warning("careful")
	if got != "⚠ careful" {
		t.Errorf("Warning(\"careful\") = %q, want %q", got, "⚠ careful")
	}
}

func TestFormatError(t *testing.T) {
	got := FormatError("bad", "details here", "try this")
	if !strings.Contains(got, "Error: bad") {
		t.Errorf("FormatError missing brief: %q", got)
	}
	if !strings.Contains(got, "details here") {
		t.Errorf("FormatError missing detail: %q", got)
	}
	if !strings.Contains(got, "Suggestion: try this") {
		t.Errorf("FormatError missing suggestion: %q", got)
	}
}

func TestFormatErrorMinimal(t *testing.T) {
	got := FormatError("oops", "", "")
	if !strings.Contains(got, "Error: oops") {
		t.Errorf("FormatError missing brief: %q", got)
	}
	if strings.Contains(got, "Suggestion:") {
		t.Errorf("FormatError should not have suggestion: %q", got)
	}
}

func TestStep(t *testing.T) {
	got := Step(1, 3, "Discovering")
	if !strings.Contains(got, "Step 1/3") {
		t.Errorf("Step missing counter: %q", got)
	}
	if !strings.Contains(got, "Discovering") {
		t.Errorf("Step missing title: %q", got)
	}
}

func TestLabel(t *testing.T) {
	got := Label("model:", 15)
	if len(got) < 15 {
		t.Errorf("Label too short: %q (len %d)", got, len(got))
	}
	if !strings.HasPrefix(got, "model:") {
		t.Errorf("Label missing text: %q", got)
	}
}

func TestCommandList(t *testing.T) {
	cmds := []CommandEntry{
		{Name: "test", Run: "npm test", Description: "run tests"},
		{Name: "lint", Run: "npm run lint"},
	}
	got := CommandList(cmds)
	if !strings.Contains(got, "test") {
		t.Errorf("CommandList missing test: %q", got)
	}
	if !strings.Contains(got, "npm test") {
		t.Errorf("CommandList missing run: %q", got)
	}
	if !strings.Contains(got, "lint") {
		t.Errorf("CommandList missing lint: %q", got)
	}
}

func TestCommandListEmpty(t *testing.T) {
	got := CommandList(nil)
	if got != "" {
		t.Errorf("CommandList(nil) = %q, want empty", got)
	}
}
