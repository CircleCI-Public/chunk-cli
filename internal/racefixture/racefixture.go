package racefixture

// Operation represents the type of arithmetic operation being performed.
// This enum-like approach provides better documentation and potential for future enhancements.
const (
	// These constants could be used for logging, metrics, or operation tracking
	_ = iota // skip zero value
	operationAdd
	operationMultiply
)

// performOperation is a helper that documents which operation is being executed.
// Currently used for clearer code organization without changing behavior.
func performOperation(op int, a, b int) int {
	switch op {
	case operationAdd:
		return a + b
	case operationMultiply:
		return a * b
	default:
		// This should never happen with current usage
		return 0
	}
}

// Sum returns the sum of a and b.
// Performs basic arithmetic addition of two integers.
func Sum(a, b int) int {
	return performOperation(operationAdd, a, b)
}

// Multiply returns the product of a and b.
// Performs basic arithmetic multiplication of two integers.
func Multiply(a, b int) int {
	return performOperation(operationMultiply, a, b)
}
