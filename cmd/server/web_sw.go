package main

import (
	"bytes"
	"strings"
)

// swVersionPlaceholder 是两份 Service Worker 源码里预留的版本占位符。
const swVersionPlaceholder = "__AIOPS_VERSION__"

// stampServiceWorker 把 SW 源码里的版本占位符替换成当前 appVersion。
//
// 为什么必须在服务端做：Service Worker 的更新判定是**按字节比对 sw.js**。两份 SW 里
// 的缓存名此前都是写死的常量，于是文件永远一模一样——浏览器永远不会认为有新版本：
//
//  1. SW 自身的逻辑缺陷装上去就再也修不掉（新版本推不下去）；
//  2. activate 从不触发，旧版本的哈希 chunk 全部留在缓存里累积，只增不减。
//
// 盖上版本戳之后，一次发版 = 一份新的 sw.js = 一轮正常的 SW 更新与旧缓存回收。
//
// 未注入版本号的开发构建里 appVersion 是占位串 "AIOps"，此时戳记恒定——这正是想要的：
// 本地反复构建不该让浏览器每次都重装 SW。
func stampServiceWorker(src []byte) []byte {
	return bytes.ReplaceAll(src, []byte(swVersionPlaceholder), []byte(swCacheVersion()))
}

// swCacheVersion 把 appVersion 归一成只含缓存名允许字符的短串。
func swCacheVersion() string {
	v := strings.TrimSpace(appVersion)
	if v == "" {
		v = "dev"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, v)
}

// v2ConsoleEmbedded 报告这个二进制里有没有打进 Vue 控制台产物。
//
// 产物由 `npm run build` 写进 cmd/server/web/v2（.gitignore 排除，靠构建生成），所以
// 同一份源码既可能构建出带 /v2 的二进制，也可能构建出不带的。前端要据此决定是否显示
// 入口——路由那边同样按这个条件注册（见 Routes）。
func v2ConsoleEmbedded() bool {
	_, err := webFS.ReadFile("web/v2/index.html")
	return err == nil
}
