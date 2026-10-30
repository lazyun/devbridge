package git

import "fmt"

// CommandError preserves bounded Git diagnostics without exposing shell state.
type CommandError struct {
	Operation string
	Args      []string
	Stderr    string
	Err       error
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("git %s failed: %v: %s", e.Operation, e.Err, e.Stderr)
	}
	return fmt.Sprintf("git %s failed: %v", e.Operation, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

type OutputLimitError struct {
	Operation string
	Limit     int
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("git %s output exceeded %d bytes", e.Operation, e.Limit)
}
