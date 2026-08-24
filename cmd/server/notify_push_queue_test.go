package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 一条发不出去的通知渠道，绝不能把**告警评估**一起拖停。
//
// 这正是机房级故障的形态：同一个网络问题让上百台主机同时离线，也让 webhook 连不上。
// 旧实现在 tick() 里串行地等每一次 8 秒超时，于是最需要告警的十几分钟里，
// 平台既发现不了新的危急告警，也判不出恢复。
func TestEnqueuePushDoesNotBlockEvaluation(t *testing.T) {
	n := NewNotifier(NewStore(), newTestConfigStore(t))

	// 拿一个"永远卡住"的投递协程占住队列出口：模拟渠道超时。
	release := make(chan struct{})
	var once sync.Once
	n.pushOnce.Do(func() {
		n.pushQ = make(chan notifyJob, notifyPushQueue)
		go func() {
			for range n.pushQ {
				once.Do(func() { <-release })
			}
		}()
	})
	t.Cleanup(func() { close(release) })

	cfg := n.cfg.Get()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			n.enqueuePush(cfg, Alert{HostID: fmt.Sprintf("h%d", i), Message: "down"}, true)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("投递卡住时，入队仍然把评估协程堵死了")
	}
}

// 队列满时必须丢弃并计数，而不是把评估协程堵在那里等 —— 也不能无限堆积到 OOM。
func TestEnqueuePushDropsWhenQueueFull(t *testing.T) {
	n := NewNotifier(NewStore(), newTestConfigStore(t))
	// 起一个不消费的队列：所有任务都进得去，直到满。
	n.pushOnce.Do(func() { n.pushQ = make(chan notifyJob, notifyPushQueue) })

	cfg := n.cfg.Get()
	total := notifyPushQueue + 25
	start := time.Now()
	for i := 0; i < total; i++ {
		n.enqueuePush(cfg, Alert{HostID: "h", Message: "down"}, true)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("入队不该阻塞，耗时 %v", el)
	}
	if got := n.pushDrop.Load(); got != uint64(total-notifyPushQueue) {
		t.Fatalf("丢弃计数应为 %d，实际 %d", total-notifyPushQueue, got)
	}
	if len(n.pushQ) != notifyPushQueue {
		t.Fatalf("队列应被填满到 %d，实际 %d", notifyPushQueue, len(n.pushQ))
	}
}

// 正常情况下任务确实会被投递协程取走（不是入队即丢）。
func TestEnqueuePushDeliversToWorker(t *testing.T) {
	n := NewNotifier(NewStore(), newTestConfigStore(t))
	got := make(chan notifyJob, 4)
	n.pushOnce.Do(func() {
		n.pushQ = make(chan notifyJob, notifyPushQueue)
		go func() {
			for j := range n.pushQ {
				got <- j
			}
		}()
	})
	n.enqueuePush(n.cfg.Get(), Alert{HostID: "h9", Message: "cpu high"}, true)
	select {
	case j := <-got:
		if j.alert.HostID != "h9" || !j.firing {
			t.Fatalf("投递内容不对：%+v", j.alert)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("任务没有被投递协程取走")
	}
}
