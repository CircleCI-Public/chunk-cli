package racefixture

// validateInputs is a helper function that can be used to validate arithmetic operation inputs.
// Currently it's a no-op but could be extended for validation logic in the future.
func validateInputs(a, b int) {
	// No validation currently required for basic arithmetic operations
	_ = a
	_ = b
}

// Sum performs integer addition and returns the sum of a and b.
// This function is designed to be race-safe for concurrent testing scenarios.
func Sum(a, b int) int {
	validateInputs(a, b)
	return a + b
}

// Multiply performs integer multiplication and returns the product of a and b.
// This function is designed to be race-safe for concurrent testing scenarios.
func Multiply(a, b int) int {
	validateInputs(a, b)
	return a * b
}
