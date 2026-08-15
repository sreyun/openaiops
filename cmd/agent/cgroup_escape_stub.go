//go:build !linux

package main

func escapeAgentCgroup(int) bool { return false }

func escapeAgentCgroupTree(int) {}
