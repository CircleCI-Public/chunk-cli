package usererr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd/usererr"
)

func TestError(t *testing.T) {
	underlying := fmt.Errorf("connection refused")
	ue := usererr.New("Could not reach the server.", underlying)

	t.Run("Error delegates to wrapped", func(t *testing.T) {
		got := ue.Error()
		want := "connection refused"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("UserMessage", func(t *testing.T) {
		got := ue.UserMessage()
		want := "Could not reach the server."
		if got != want {
			t.Errorf("UserMessage() = %q, want %q", got, want)
		}
	})

	t.Run("Unwrap", func(t *testing.T) {
		if !errors.Is(ue.Unwrap(), underlying) {
			t.Error("Unwrap() did not return the underlying error")
		}
	})

	t.Run("errors.Is", func(t *testing.T) {
		if !errors.Is(ue, underlying) {
			t.Error("errors.Is failed to match underlying error")
		}
	})

	t.Run("errors.As", func(t *testing.T) {
		// Wrap in another error to test errors.As unwrapping.
		wrapped := fmt.Errorf("outer: %w", ue)
		var target *usererr.Error
		if !errors.As(wrapped, &target) {
			t.Fatal("errors.As failed to find *usererr.Error")
		}
		if target.UserMessage() != "Could not reach the server." {
			t.Errorf("UserMessage() = %q after errors.As", target.UserMessage())
		}
	})
}

func TestNewDetailed(t *testing.T) {
	cause := fmt.Errorf("root")
	ue := usererr.NewDetailed("Brief msg.", "Some detail.", "Try this.", cause)
	if ue.UserMessage() != "Brief msg." {
		t.Errorf("UserMessage() = %q", ue.UserMessage())
	}
	if ue.Detail() != "Some detail." {
		t.Errorf("Detail() = %q", ue.Detail())
	}
	if ue.Suggestion() != "Try this." {
		t.Errorf("Suggestion() = %q", ue.Suggestion())
	}
	if !errors.Is(ue, cause) {
		t.Error("Unwrap chain broken")
	}
}

func TestNewf(t *testing.T) {
	sentinel := fmt.Errorf("root cause")
	ue := usererr.Newf("Could not save file.", "save %s: %w", "config.json", sentinel)

	if got := ue.UserMessage(); got != "Could not save file." {
		t.Errorf("UserMessage() = %q", got)
	}
	if got := ue.Error(); got != "save config.json: root cause" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(ue, sentinel) {
		t.Error("errors.Is failed to match wrapped sentinel")
	}
}
