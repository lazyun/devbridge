package codexlaunch

// ExecutorArgs applies only to Bridge-owned children. It never writes the user's
// Desktop/CLI configuration. Per-thread MCP isolation is applied after resolving
// the effective workspace configuration, including project layers.
func ExecutorArgs(args []string) []string {
	result := append([]string(nil), args...)
	return append(result,
		"-c", "features.apps=false",
		"-c", "features.plugins=false",
		"-c", "features.remote_plugin=false",
		"-c", "features.skill_mcp_dependency_install=false",
	)
}
