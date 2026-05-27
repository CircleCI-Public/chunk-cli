package racefixture

import (
	"errors"
	"fmt"
)

// ErrNegative is returned when a value is below zero.
var ErrNegative = errors.New("negative value")

// ValidatePositive returns an error if n is negative.
func ValidatePositive(n int) error {
	if n < 0 {
		return fmt.Errorf("racefixture: %w", ErrNegative)
	}
	return nil
}
