package acceptance

import (
	"fmt"
	"os"
	"testing"

	"github.com/smeltdev/smelt/internal/testing/binary"
)

func TestMain(m *testing.M) {
	path, err := binary.BuildBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping acceptance tests: %v\n", err)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "built smelt binary: %s\n", path)
	os.Exit(m.Run())
}
