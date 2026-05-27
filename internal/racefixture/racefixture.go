package racefixture

import "fmt"

// Sum performs addition and returns the sum of two integers a and b.
// This function is used for testing race conditions and concurrent access patterns.
func Sum(a, b int) int {
	result, err := performOperation(a, b, func(x, y int) int { return x + y })
	if err != nil {
		// This should never happen with a valid operation function, but we handle it defensively
		panic(fmt.Sprintf("unexpected error in Sum: %v", err))
	}
	return result
}

// Multiply performs multiplication and returns the product of two integers a and b.
// This function is used for testing race conditions and concurrent access patterns.
func Multiply(a, b int) int {
	result, err := performOperation(a, b, func(x, y int) int { return x * y })
	if err != nil {
		// This should never happen with a valid operation function, but we handle it defensively
		panic(fmt.Sprintf("unexpected error in Multiply: %v", err))
	}
	return result
}

// performOperation is a helper that applies a mathematical operation to two integers.
// This abstraction helps demonstrate patterns for concurrent mathematical operations.
// Returns an error if the operation function is nil.
func performOperation(a, b int, op func(int, int) int) (int, error) {
	if op == nil {
		return 0, fmt.Errorf("operation function cannot be nil")
	}
	return op(a, b), nil
}
