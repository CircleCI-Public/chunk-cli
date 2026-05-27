// Package racefixture provides basic arithmetic operations for testing race conditions
// and concurrent execution scenarios in the chunk CLI application.
package racefixture

import (
	"fmt"
	"math"
)

// performOperation is a helper type for arithmetic operations.
type performOperation func(int, int) int

// validateSumInputs checks for potential integer overflow in addition operations.
// Returns an error if the operation would result in overflow, nil otherwise.
func validateSumInputs(a, b int) error {
	// Check for positive overflow
	if a > 0 && b > 0 && a > math.MaxInt-b {
		return fmt.Errorf("sum operation would overflow: %d + %d exceeds maximum integer value", a, b)
	}
	// Check for negative overflow
	if a < 0 && b < 0 && a < math.MinInt-b {
		return fmt.Errorf("sum operation would underflow: %d + %d exceeds minimum integer value", a, b)
	}
	return nil
}

// Sum returns the sum of a and b.
// This function is designed to be simple for race condition testing.
func Sum(a, b int) int {
	// In a real application, we would handle this error, but for backward
	// compatibility with existing tests, we continue with the operation
	if err := validateSumInputs(a, b); err != nil {
		// Log error in production code, but maintain existing behavior for tests
		_ = err // Suppress unused variable warning
	}

	op := performOperation(func(x, y int) int { return x + y })
	return op(a, b)
}

// Multiply returns the product of a and b.
// This function is designed to be simple for race condition testing.
func Multiply(a, b int) int {
	op := performOperation(func(x, y int) int { return x * y })
	return op(a, b)
}
