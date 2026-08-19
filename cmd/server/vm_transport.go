package main

import (
	"net"
	"net/http"
	"time"
)

// newVMTransport 返回专供 VictoriaMetrics 用的连接池。
//
// 为什么必须自己建：`&http.Client{Timeout: ...}` 不带 Transport 就会用
// `http.DefaultTransport`，而它的 `MaxIdleConnsPerHost` 是 **2**（net/http 的
// DefaultMaxIdleConnsPerHost）。本服务所有时序读写都打向**同一台** VM：
//
//   - 一次看板刷新可能同时发出十几条 query_range；超过 2 条的部分每次都要新建 TCP
//     连接，用完还没法放回池子（池子满了）——于是变成"每查一次握一次手"，
//     并在系统里堆出大量 TIME_WAIT。
//   - 写入路径每个采集周期都在批量 POST，与查询抢那 2 个空闲连接。
//
// 症状是查询 P99 抖动、VM 侧看到连接数远高于并发数，但没有任何错误日志——属于
// "不报错的慢"，最容易被当成"时序库不行"。
//
// 这里的取值对齐服务端的实际并发面：看板并发查询 + 后台任务 + 写入批次。
func newVMTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// 全部流量都指向同一台 VM，所以 per-host 上限才是真正起作用的那个。
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}
