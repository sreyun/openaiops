//go:build !windows

package main

// 非 Windows 平台没有盘符：所有本地文件系统都挂在同一棵树下，从 "/" 走下去就能覆盖，
// 网络/伪文件系统由 fimRemoteMountExcludes 与默认排除项挡掉。
func fimLocalDriveRoots() []string { return nil }
