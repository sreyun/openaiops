//go:build ignore

package main

import (
	"io"
	"os"
	"path/filepath"
)

// Cross-platform stand-in for `cp` —— 与 cmd/agent/copy_example.go 同构。
//
// 这里原来直接写 `//go:generate cp ...`：Windows 上没有 GNU cp，于是
// `go generate ./...`（CLAUDE.md 里改完根目录 config.example.yaml 必须跑的那条）
// 在 Windows 上必然报 `exec: "cp": executable file not found`，而 cmd/agent 那份
// 早就是 go run 了——两个副本一个能同步一个不能，是最容易漏同步的形态。
func main() {
	src := filepath.Join("..", "..", "config.example.yaml")
	dst := "config_example.yaml"
	in, err := os.Open(src)
	if err != nil {
		panic(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		panic(err)
	}
}
