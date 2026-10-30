package workspace

import "errors"

var (
	ErrNotFound = errors.New("workspace not found")
	ErrConflict = errors.New("workspace conflict")
)
