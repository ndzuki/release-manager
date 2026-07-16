package expired_exception

import "os/exec" // want `os_exec_import`

// This should trigger forbidden_binary_invocation because the exception is expired (AC-037-04).
func installHelm() error {
	cmd := exec.Command("helm", "list") // want `forbidden_binary_invocation.*helm`
	return cmd.Run()
}
