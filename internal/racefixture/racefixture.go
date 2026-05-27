// Package racefixture provides basic arithmetic operations for testing purposes.
// It serves as a simple fixture for demonstrating race condition testing patterns.
package racefixture

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
