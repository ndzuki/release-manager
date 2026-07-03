//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Ensure cleanup runs even when newHarnessInternal panics partway through.
	var panicErr interface{}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "TestMain setup failed: %v\n", r)
			panicErr = r
		}
		if testHarness != nil {
			testHarness.Close()
		}
		// Re-panic so os.Exit(1) is reached below, or exit now if setup failed
		if panicErr != nil {
			os.Exit(1)
		}
	}()

	newHarnessInternal()

	code := m.Run()
	testHarness.Close()
	os.Exit(code)
}
