package main

import (
	"bytes"
	"testing"
	"time"
)

func TestDeskFrameRoundTrip(t *testing.T) {
	f := deskFrame('M', []byte(`{"x":1}`))
	if f[0] != 'M' {
		t.Fatalf("type %v", f[0])
	}
	if len(f) != 3+len([]byte(`{"x":1}`)) {
		t.Fatalf("len %d", len(f))
	}
}

func TestDeskManagerNotifyPending(t *testing.T) {
	m := newDeskManager()
	s := m.create("h1", "host1", "op", "1.1.1.1", "zh")
	ok, alive := m.notifyAgent("h1", s.id)
	if !ok {
		t.Fatal("notify failed")
	}
	if alive {
		t.Fatal("without recent waiter, channel should look dead")
	}
	// no waiter → pending
	m.mu.Lock()
	n := len(m.pendingSessions["h1"])
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("pending=%d", n)
	}
	m.remove(s.id)
	time.Sleep(10 * time.Millisecond)
}

// drainToBrowser collects everything currently queued for the browser.
func drainToBrowser(s *deskSession) [][]byte {
	var out [][]byte
	for {
		select {
		case b := <-s.toBrowser:
			out = append(out, b)
		default:
			return out
		}
	}
}

// A flood of video frames on a full queue must never evict a control/error frame.
// This guards the "一点开就已断开" regression where a racing error frame ('E')
// got dropped and the UI only saw a bare WebSocket close.
func TestDeskEnqueuePreservesControlFrames(t *testing.T) {
	s := &deskSession{toBrowser: make(chan []byte, 4), done: make(chan struct{})}

	// Fill the queue: one error frame first, then video frames.
	if !s.enqueueBrowser([]byte("E{\"error\":\"boom\"}")) {
		t.Fatal("enqueue error frame failed")
	}
	for i := 0; i < 3; i++ {
		if !s.enqueueBrowser([]byte("Kvideo")) {
			t.Fatal("enqueue video failed")
		}
	}
	// Queue is now full (cap 4). Flood with more video frames.
	for i := 0; i < 50; i++ {
		if !s.enqueueBrowser([]byte("Kmore")) {
			t.Fatal("flood enqueue returned done unexpectedly")
		}
	}

	frames := drainToBrowser(s)
	sawError := false
	for _, f := range frames {
		if len(f) > 0 && f[0] == 'E' {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("error frame was evicted by video flood; queue=%d frames", len(frames))
	}
}

// Newest video frame should win when the queue overflows with video-only frames.
func TestDeskEnqueuePrefersNewestVideo(t *testing.T) {
	s := &deskSession{toBrowser: make(chan []byte, 2), done: make(chan struct{})}
	if !s.enqueueBrowser([]byte("Kold1")) || !s.enqueueBrowser([]byte("Kold2")) {
		t.Fatal("initial enqueue failed")
	}
	if !s.enqueueBrowser([]byte("Knew")) {
		t.Fatal("overflow enqueue failed")
	}
	frames := drainToBrowser(s)
	last := frames[len(frames)-1]
	if string(last) != "Knew" {
		t.Fatalf("newest video not preserved, got %q", last)
	}
}

// After close(), enqueue must report done so the relay loop can stop.
func TestDeskEnqueueStopsOnDone(t *testing.T) {
	s := &deskSession{toBrowser: make(chan []byte), done: make(chan struct{})}
	s.close()
	if s.enqueueBrowser([]byte("S{}")) {
		t.Fatal("expected enqueueBrowser to return false after close")
	}
	// close is idempotent
	s.close()
}

// 画面积压 = 延迟。原来的队列有 256 格，20fps 下等于十几秒的缓冲——用户感受到的
// "点一下等三五秒"有一半来自这里。现在超过 deskBrowserVideoBacklog 就把旧画面全丢掉，
// 只留最新的：差分帧被丢那一块会停在旧像素上，但 Agent 每 5 秒必发一张整帧关键帧，
// 几秒内自愈；相比之下"整体落后十几秒"是不可用的。
func TestDeskEnqueueDropsStaleVideoBacklog(t *testing.T) {
	s := &deskSession{toBrowser: make(chan []byte, 64), done: make(chan struct{})}
	for i := 0; i < 50; i++ {
		if !s.enqueueBrowser([]byte("Kframe")) {
			t.Fatal("enqueue video failed")
		}
	}
	if n := len(s.toBrowser); n > deskBrowserVideoBacklog+1 {
		t.Fatalf("画面积压了 %d 帧（上限 %d）——这就是延迟本身", n, deskBrowserVideoBacklog+1)
	}
}

// 差分帧（'T'）与整帧、H.264 一样是"可以丢的画面"，不能被当成控制帧堆在队列里。
func TestDeskTileFramesCountAsVideo(t *testing.T) {
	if !deskIsVideoFrame([]byte("Ttiles")) {
		t.Fatal("'T' 差分帧必须按画面帧处理（积压时可丢）")
	}
	if deskIsVideoFrame([]byte("S{}")) || deskIsVideoFrame([]byte("E{}")) {
		t.Fatal("控制帧不该被当成画面帧")
	}
}

// 纯移动的鼠标事件在上行积压时要丢掉，按下/抬起一个都不能丢。
func TestDeskMouseMoveDetection(t *testing.T) {
	if !deskIsMouseMove([]byte(`{"x":1,"y":2,"action":"move","btn":0}`)) {
		t.Fatal("纯移动没被识别出来")
	}
	for _, p := range []string{`{"action":"down","btn":1}`, `{"action":"up","btn":1}`, `{"action":"click"}`} {
		if deskIsMouseMove([]byte(p)) {
			t.Fatalf("%s 被误判成纯移动——按键会被丢掉", p)
		}
	}
}

// Windows 登录/注销/切换用户会让服务在新会话里重开桌面 worker。旧 worker 的 tx 一断，
// 服务端过去直接关掉整个会话，浏览器只能关掉重开——这就是现场那句"输完用户名密码
// 要重新进一次"。现在改成留住会话、重新排进待接管队列。
func TestDeskRearmKeepsSessionForNewWorker(t *testing.T) {
	m := newDeskManager()
	sess := m.create("h1", "host1", "op", "1.1.1.1", "zh")
	defer m.remove(sess.id)

	if !m.rearmAgent(sess) {
		t.Fatal("浏览器还连着，会话就不该被判死")
	}
	select {
	case <-sess.done:
		t.Fatal("rearm 之后会话被关掉了")
	default:
	}
	m.mu.Lock()
	pending := len(m.pendingSessions["h1"])
	m.mu.Unlock()
	if pending == 0 {
		t.Fatal("会话没有重新排进待接管队列，新 worker 永远接不到它")
	}
	// 浏览器要看到"正在恢复"，而不是一句"已断开"。
	var sawPhase bool
	for _, f := range drainToBrowser(sess) {
		if len(f) > 0 && f[0] == 'S' && bytes.Contains(f, []byte("agent_reconnecting")) {
			sawPhase = true
		}
	}
	if !sawPhase {
		t.Fatal("没有给浏览器发 agent_reconnecting，界面会显示成断线")
	}

	// 浏览器已经走了就别再等新 worker。
	sess2 := m.create("h2", "host2", "op", "1.1.1.1", "zh")
	sess2.close()
	if m.rearmAgent(sess2) {
		t.Fatal("浏览器都关了还在等 worker 接管")
	}
}

// 反复重开也要收敛：worker 起来就崩、崩了又起时，不能让用户对着"正在恢复"无限干等。
func TestDeskRearmIsBounded(t *testing.T) {
	m := newDeskManager()
	sess := m.create("h1", "host1", "op", "1.1.1.1", "zh")
	defer m.remove(sess.id)
	for i := 0; i < deskMaxRearms; i++ {
		if !m.rearmAgent(sess) {
			t.Fatalf("第 %d 次重新接管就放弃了，太早", i+1)
		}
	}
	if m.rearmAgent(sess) {
		t.Fatal("超过上限之后应当停止重试")
	}
}
