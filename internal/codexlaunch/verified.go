package codexlaunch

import "context"

// VerifiedExecutable is a transient launch capability. Each call rechecks its
// captured constraints and returns the exact native path without consulting
// Settings, PATH or a package launcher. Platform tokens check artifact bytes;
// qualified Execution tokens additionally check the original live authority.
type VerifiedExecutable interface {
	Executable(context.Context) (string, error)
}
