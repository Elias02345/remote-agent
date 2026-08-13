package main

import "testing"

// A wildcard bind is the one mistake that turns "no authentication yet" from a
// deliberate Phase 4 state into an open shell on the network.
func TestCheckBindAddressRejectsWildcards(t *testing.T) {
	bad := []string{"0.0.0.0:8080", ":8080", "[::]:8080", "*:8080"}
	for _, addr := range bad {
		if err := checkBindAddress(addr); err == nil {
			t.Errorf("checkBindAddress(%q) = nil, want an error", addr)
		}
	}
}

func TestCheckBindAddressAcceptsSpecificHosts(t *testing.T) {
	good := []string{"127.0.0.1:8080", "localhost:8080", "100.101.102.103:8080", "[::1]:8080"}
	for _, addr := range good {
		if err := checkBindAddress(addr); err != nil {
			t.Errorf("checkBindAddress(%q) = %v, want nil", addr, err)
		}
	}
}

func TestCheckBindAddressRejectsGarbage(t *testing.T) {
	if err := checkBindAddress("not-an-address"); err == nil {
		t.Error("expected an error for a malformed address")
	}
}
