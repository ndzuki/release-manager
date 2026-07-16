package shell_wrapper

import "os/exec" // want `os_exec_import`

// hiddenWrapper simulates a shell wrapper that invokes helm.
// This should trigger shell_wrapper rule.
func hiddenWrapper() error {
	cmd := exec.Command("bash", "-c", "helm install my-release ./chart") // want `shell_wrapper.*shell_wrapper.go:8`
	return cmd.Run()
}
