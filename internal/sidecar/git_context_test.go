package sidecar

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestCreateBundleHonoursCanceledContext(t *testing.T) {
	_, err := createBundle(canceledContext(), "", t.TempDir())
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestGeneratePatchHonoursCanceledContext(t *testing.T) {
	_, err := generatePatch(canceledContext(), "HEAD", t.TempDir())
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestBranchPushedHonoursCanceledContext(t *testing.T) {
	_, err := branchPushed(canceledContext(), t.TempDir())
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestMergeBaseHonoursCanceledContext(t *testing.T) {
	_, err := mergeBase(canceledContext(), t.TempDir())
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}
