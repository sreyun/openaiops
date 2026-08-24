package main

// v2ConsoleEmbedded returns whether the Vue v2 console is embedded in this build.
// 开源版本不包含 v2 前端，始终返回 false。
func v2ConsoleEmbedded() bool {
	return false
}
