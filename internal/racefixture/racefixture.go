// Package racefixture provides basic arithmetic operations for testing purposes.
// It serves as a simple fixture for demonstrating race condition testing patterns.
package racefixture

import "fmt"

// Sum returns the sum of a and b.
// Example: Sum(2, 3) returns 5.
func Sum(a, b int) int {
	return a + b
}

// Multiply returns the product of a and b.
// Example: Multiply(3, 4) returns 12.
func Multiply(a, b int) int {
	return a * b
}

// Divide returns the division of a by b.
// Returns an error if b is zero.
// Example: Divide(8, 2) returns 4, nil.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, fmt.Errorf("division operation failed: cannot divide %d by zero (denominator must be non-zero)", a)
	}
	return a / b, nil
}
