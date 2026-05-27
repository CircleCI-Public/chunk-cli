// Package racefixture provides basic arithmetic functions for testing purposes.
// This package is primarily used as a test fixture for race condition detection
// and code formatting validation.
package racefixture

import (
	"fmt"
	"math"
)

// Sum performs integer addition and returns the sum of two operands.
func Sum(a, b int) int {
	return a + b
}

// Multiply performs integer multiplication and returns the product of two operands.
// Returns an error if the multiplication would result in integer overflow.
func Multiply(a, b int) (int, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}

	// Check for overflow before performing multiplication
	if a > 0 && b > 0 && a > math.MaxInt/b {
		return 0, fmt.Errorf("multiplication overflow: %d * %d would exceed maximum int value", a, b)
	}
	if a < 0 && b < 0 && a < math.MaxInt/b {
		return 0, fmt.Errorf("multiplication overflow: %d * %d would exceed maximum int value", a, b)
	}
	if (a > 0 && b < 0 && b < math.MinInt/a) || (a < 0 && b > 0 && a < math.MinInt/b) {
		return 0, fmt.Errorf("multiplication underflow: %d * %d would exceed minimum int value", a, b)
	}

	return a * b, nil
}
