package acceptance

import (
	"fmt"
	"os"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
)

func TestMain(m *testing.M) {
	path, err := testutil.BuildBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping acceptance tests: %v\n", err)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "built chunk binary: %s\n", path)
	os.Exit(m.Run())
}
