//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "TestMain setup failed: %v\n", r)
			os.Exit(1)
		}
	}()

	_ = err
	testHarness = newHarnessInternal()
	defer testHarness.Close()

	code := m.Run()
	os.Exit(code)
}
