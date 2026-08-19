package main

import (
	"context"
	"testing"
)

func TestContainerActionValidation(t *testing.T) {
	if _, code := moduleContainerAction(context.Background(), map[string]string{}); code == 0 {
		t.Fatal("missing args should fail")
	}
	// Without docker/podman, should fail with clear message
	out, code := moduleContainerAction(context.Background(), map[string]string{"action": "start", "id": "abc"})
	if code == 0 {
		t.Fatalf("expected fail without runtime, got %s", out)
	}
}

func TestContainerLogsValidation(t *testing.T) {
	if _, code := moduleContainerLogs(context.Background(), map[string]string{}); code == 0 {
		t.Fatal("missing id should fail")
	}
}
