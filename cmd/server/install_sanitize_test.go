package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeServerURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://192.168.1.10:8080", "http://192.168.1.10:8080"},
		{"https://panel.example.com/", "https://panel.example.com"},
		{"http://[2001:db8::1]:8529", "http://[2001:db8::1]:8529"},
		{"http://user:pass@evil.com", ""},
		{"ftp://x.example.com", ""},
		{"http://host/path?x=1", "http://host"},
		{"not-a-url", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := sanitizeServerURL(tc.in)
		if got != tc.want {
			t.Fatalf("sanitizeServerURL(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeServersJSON(t *testing.T) {
	raw := `[{"server":"http://a:1","token":"tok-a"},{"server":"http://[2001:db8::1]:8529","token":"tok-b"},{"server":"ftp://bad","token":"x"},{"server":"http://a:1","token":"dup"}]`
	got := sanitizeServersJSON(raw)
	if got == "" {
		t.Fatal("expected non-empty servers json")
	}
	for _, want := range []string{"http://a:1", "http://[2001:db8::1]:8529", "tok-a", "tok-b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "ftp://") {
		t.Fatalf("should drop bad scheme: %s", got)
	}
	if strings.Count(got, "http://a:1") != 1 {
		t.Fatalf("should dedupe primary: %s", got)
	}

	many := "["
	for i := 0; i < 20; i++ {
		if i > 0 {
			many += ","
		}
		many += fmt.Sprintf(`{"server":"http://h%d:8529","token":"t"}`, i)
	}
	many += "]"
	got = sanitizeServersJSON(many)
	if n := strings.Count(got, `"server"`); n != maxServersJSONEntries {
		t.Fatalf("entry cap: got %d want %d in %s", n, maxServersJSONEntries, got)
	}
}
