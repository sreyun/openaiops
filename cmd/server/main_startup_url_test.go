package main

import "testing"

// The startup banner is the first screen a new operator sees. Concatenating
// "http://localhost" with an -addr that already carries a host printed
// http://localhost127.0.0.1:8529 — an un-clickable address in the very first
// impression of the product.
func TestStartupDisplayHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":8529", "localhost:8529"},
		{"8529", "localhost:8529"},
		{"0.0.0.0:8529", "localhost:8529"},
		{"[::]:8529", "localhost:8529"},
		{"127.0.0.1:8529", "127.0.0.1:8529"},
		{"192.168.1.10:8080", "192.168.1.10:8080"},
		{"", "localhost"},
	} {
		if got := startupDisplayHost(tc.in); got != tc.want {
			t.Errorf("startupDisplayHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
