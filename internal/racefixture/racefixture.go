package racefixture

import (
	"fmt"
	"math"
)

// Sum returns the sum of a and b.
//
// Example:
//
//	result := Sum(3, 5) // result is 8
func Sum(a, b int) int {
	return a + b
}

// Multiply returns the product of a and b.
// Panics if the multiplication would overflow.
//
// Example:
//
//	result := Multiply(3, 4) // result is 12
func Multiply(a, b int) int {
	// Check for overflow conditions
	if a != 0 && (a > math.MaxInt/abs(b) || a < math.MinInt/abs(b)) {
		panic(fmt.Errorf("integer overflow: multiplying %d * %d would exceed int bounds", a, b))
	}
	return a * b
}

// abs returns the absolute value of x
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
