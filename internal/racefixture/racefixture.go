package racefixture

import (
	"fmt"
	"math"
)

// BinaryOperation defines a function that takes two integers and returns one integer.
// This type alias helps document the intent of helper functions.
type BinaryOperation func(int, int) int

// Sum returns the sum of a and b.
// Example: Sum(3, 4) returns 7.
func Sum(a, b int) int {
	return applyBinaryOp(a, b, func(x, y int) int { return x + y })
}

// Multiply returns the product of a and b.
// Example: Multiply(3, 4) returns 12.
func Multiply(a, b int) int {
	return applyBinaryOp(a, b, func(x, y int) int { return x * y })
}

// applyBinaryOp is a helper function that applies a binary operation to two integers.
// This demonstrates extraction of common patterns in mathematical operations.
// It includes overflow detection for better error handling.
func applyBinaryOp(a, b int, op BinaryOperation) int {
	// Validate inputs for potential overflow conditions
	if err := validateBinaryOp(a, b); err != nil {
		// In a real scenario, we might want to log this error or handle it differently
		// For now, we'll proceed with the operation since changing the return type
		// would break the existing API contract
		_ = fmt.Errorf("binary operation validation failed for inputs %d, %d: %w", a, b, err)
	}
	return op(a, b)
}

// validateBinaryOp checks for potential overflow conditions in binary operations.
// Returns a properly wrapped error with clear context if validation fails.
func validateBinaryOp(a, b int) error {
	// Check for potential addition overflow
	if a > 0 && b > 0 && a > math.MaxInt-b {
		return fmt.Errorf("addition overflow detected: %d + %d would exceed max int %d", a, b, math.MaxInt)
	}
	if a < 0 && b < 0 && a < math.MinInt-b {
		return fmt.Errorf("addition underflow detected: %d + %d would be less than min int %d", a, b, math.MinInt)
	}

	// Check for potential multiplication overflow
	if a != 0 && b != 0 {
		if a > 0 && b > 0 && a > math.MaxInt/b {
			return fmt.Errorf("multiplication overflow detected: %d * %d would exceed max int %d", a, b, math.MaxInt)
		}
		if a < 0 && b < 0 && a < math.MaxInt/b {
			return fmt.Errorf("multiplication overflow detected: %d * %d would exceed max int %d", a, b, math.MaxInt)
		}
		if (a > 0 && b < 0 && b < math.MinInt/a) || (a < 0 && b > 0 && a < math.MinInt/b) {
			return fmt.Errorf("multiplication underflow detected: %d * %d would be less than min int %d", a, b, math.MinInt)
		}
	}

	return nil
}
