package secrets

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestOpResolver_SkipIfNotInstalled(t *testing.T) {
	if _, err := exec.LookPath("op"); err != nil {
		t.Skip("op CLI not on PATH")
	}
}

func TestOpResolver_Resolve_NotInstalled(t *testing.T) {
	// Simulate op not being found by using a resolver with a bad path.
	r := &OpResolver{}
	r.once.Do(func() {
		r.opPath = ""
		r.lookErr = exec.ErrNotFound
	})

	_, err := r.Resolve(context.Background(), "op://vault/item/field")
	if err == nil {
		t.Fatal("expected error when op not installed")
	}
	if err.Error() == "" {
		t.Fatal("error message should not be empty")
	}
}

func TestOpResolver_NotInstalledMatchesSentinel(t *testing.T) {
	r := &OpResolver{}
	r.once.Do(func() { r.lookErr = exec.ErrNotFound })

	_, err := r.Resolve(context.Background(), "op://vault/item/field")
	if !errors.Is(err, ErrOpNotFound) {
		t.Fatalf("expected ErrOpNotFound, got %v", err)
	}
	// The underlying lookup cause must stay reachable for diagnostics.
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("expected wrapped exec.ErrNotFound, got %v", err)
	}
}
