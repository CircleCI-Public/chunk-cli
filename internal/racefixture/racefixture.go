// Package racefixture provides basic arithmetic operations for testing race conditions
// and ensuring proper Go formatting and linting practices.
package racefixture

import "fmt"

// Sum returns the sum of a and b.
func Sum(a, b int) int {
	return performOperation(a, b, add)
}

// Multiply returns the product of a and b.
func Multiply(a, b int) int {
	return performOperation(a, b, multiply)
}

// operationType defines the signature for arithmetic operations.
type operationType func(int, int) int

// performOperation executes the given operation on two integers.
// This helper demonstrates separation of concerns and makes the code
// more testable and maintainable.
func performOperation(a, b int, op operationType) int {
	return op(a, b)
}

// add performs addition of two integers.
func add(a, b int) int {
	return a + b
}

// multiply performs multiplication of two integers.
func multiply(a, b int) int {
	return a * b
}

// Divide returns the quotient of a and b, or an error if b is zero.
// This function demonstrates proper error handling with clear messages.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, fmt.Errorf("cannot divide %d by zero: division by zero is undefined", a)
	}
	return a / b, nil
}
