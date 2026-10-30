package mcp

import (
	"context"
	"errors"
)

type Access string

const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

type Action struct {
	Tool   string
	Access Access
}
type Resource struct {
	Type string
	ID   string
}

type Authorizer interface {
	Authorize(context.Context, Action, Resource) error
}
type AllowLocal struct{}

func (AllowLocal) Authorize(context.Context, Action, Resource) error { return nil }

var ErrPermissionDenied = errors.New("MCP action denied")
