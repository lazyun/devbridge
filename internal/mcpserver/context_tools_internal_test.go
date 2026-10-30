package mcpserver

import (
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
)

func TestPublicToolErrorDoesNotPanicForTypedNilControlError(t *testing.T) {
	var typedNil *controlapi.Error
	err := publicToolError(typedNil)
	if err == nil || err.Error() != "INTERNAL_ERROR: typed nil control-plane error" {
		t.Fatalf("error=%v", err)
	}
}
