package racefixture

// Sum performs addition and returns the sum of two integers a and b.
// This function is used for testing race conditions and concurrent access patterns.
func Sum(a, b int) int {
	return performOperation(a, b, func(x, y int) int { return x + y })
}

// Multiply performs multiplication and returns the product of two integers a and b.
// This function is used for testing race conditions and concurrent access patterns.
func Multiply(a, b int) int {
	return performOperation(a, b, func(x, y int) int { return x * y })
}

// performOperation is a helper that applies a mathematical operation to two integers.
// This abstraction helps demonstrate patterns for concurrent mathematical operations.
func performOperation(a, b int, op func(int, int) int) int {
	return op(a, b)
}
