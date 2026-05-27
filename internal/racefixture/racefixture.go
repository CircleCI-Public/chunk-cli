// Package racefixture provides simple mathematical operations
// for testing race conditions and concurrent access patterns.
package racefixture

import "fmt"

// Sum returns the sum of a and b.
func Sum(a, b int) int {
	return a + b
}

// Multiply returns the product of a and b.
func Multiply(a, b int) int {
	return a * b
}

// Calculate performs a compound operation combining addition and multiplication.
// This helper demonstrates safe concurrent mathematical operations.
func Calculate(a, b, factor int) (int, error) {
	if factor == 0 {
		return 0, fmt.Errorf("invalid factor %d: cannot use zero as multiplication factor", factor)
	}

	sum := Sum(a, b)
	result := Multiply(sum, factor)
	return result, nil
}
