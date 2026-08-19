//go:build !windows

package main

import (
	"context"
	"testing"
)

func TestHyperVOpsUnsupportedOnNonWindows(t *testing.T) {
	if _, code := moduleHyperVPower(context.Background(), map[string]string{"action": "start", "name": "x"}); code == 0 {
		t.Fatal("expected failure on non-windows")
	}
	if _, code := moduleHyperVSet(context.Background(), map[string]string{"name": "x", "processor_count": "2"}); code == 0 {
		t.Fatal("expected failure on non-windows")
	}
}
