package racefixture

import (
	"errors"
	"testing"
)

func TestValidatePositive(t *testing.T) {
	if err := ValidatePositive(-1); err == nil {
		t.Fatal("expected error for negative value")
	} else if !errors.Is(err, ErrNegative) {
		t.Fatalf("expected ErrNegative, got %v", err)
	}
	if err := ValidatePositive(0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
