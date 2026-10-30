package main

import "testing"

// The Local MCP adapter is local-first: the default listener and every normal
// product path bind loopback only. Non-loopback exposure requires the
// explicit, unsupported development escape hatch.
func TestDefaultListenAddressIsLoopback(t *testing.T) {
	if err := validateListenAddress(defaultListenAddress, false); err != nil {
		t.Fatalf("default listen address rejected: %v", err)
	}
	if defaultListenAddress != "127.0.0.1:8090" {
		t.Fatalf("default listen address = %q, want the loopback default", defaultListenAddress)
	}
}

func TestLoopbackAddressesAreAcceptedWithoutEscapeHatch(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8090", "127.0.0.1:0", "localhost:8090", "[::1]:8090"} {
		if err := validateListenAddress(address, false); err != nil {
			t.Fatalf("loopback address %q rejected: %v", address, err)
		}
	}
}

func TestNonLoopbackAddressesRequireExplicitEscapeHatch(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8090", "192.168.1.10:8090", "10.0.0.5:8090", "[fe80::1]:8090"} {
		if err := validateListenAddress(address, false); err == nil {
			t.Fatalf("non-loopback address %q accepted without the escape hatch", address)
		}
		if err := validateListenAddress(address, true); err != nil {
			t.Fatalf("explicit development escape hatch rejected %q: %v", address, err)
		}
	}
}

func TestInvalidListenAddressesAreRejected(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1", "no-address"} {
		if err := validateListenAddress(address, false); err == nil {
			t.Fatalf("invalid address %q accepted", address)
		}
	}
}
