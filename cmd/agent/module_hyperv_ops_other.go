//go:build !windows

package main

import "context"

func moduleHyperVPower(_ context.Context, args map[string]string) ([]byte, int) {
	return []byte("hyperv_power 仅支持 Windows Hyper-V 宿主机"), 1
}

func moduleHyperVSet(_ context.Context, args map[string]string) ([]byte, int) {
	return []byte("hyperv_set 仅支持 Windows Hyper-V 宿主机"), 1
}
