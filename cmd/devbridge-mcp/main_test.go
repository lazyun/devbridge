package main

import (
	"testing"
)

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if err := validateListenAddress(address, false); err != nil {
			t.Errorf("validateListenAddress(%q): %v", address, err)
		}
	}
	if err := validateListenAddress("0.0.0.0:8080", false); err == nil {
		t.Fatal("public wildcard listener accepted without opt-in")
	}
	if err := validateListenAddress("0.0.0.0:8080", true); err != nil {
		t.Fatalf("explicit public listener rejected: %v", err)
	}
}
