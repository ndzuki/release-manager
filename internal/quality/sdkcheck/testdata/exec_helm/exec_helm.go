package exec_helm

import "os/exec" // want `os_exec_import`

// This should trigger os_exec_import and forbidden_binary_invocation
func installChart() error {
	cmd := exec.Command("helm", "install", "my-release", "./chart") // want `forbidden_binary_invocation.*helm.*exec_helm.go:7`
	return cmd.Run()
}
