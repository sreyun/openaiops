package main

import (
	"sync"
	"testing"
	"time"
)

// recInput 记录注入顺序，并可以模拟"每个事件都很慢"——这正是非 Windows 平台的现实：
// 一次注入要 fork 一个 xdotool/ydotool，十几到几十毫秒。
type recInput struct {
	mu    sync.Mutex
	ops   []string
	delay time.Duration
}

func (r *recInput) rec(s string) {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.ops = append(r.ops, s)
	r.mu.Unlock()
}
func (r *recInput) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}
func (r *recInput) MouseMove(x, y int) error { r.rec("move"); return nil }
func (r *recInput) MouseButton(b int, d bool) error {
	if d {
		r.rec("down")
	} else {
		r.rec("up")
	}
	return nil
}
func (r *recInput) MouseWheel(d int) error { r.rec("wheel"); return nil }
func (r *recInput) Key(vk int, d bool) error {
	if d {
		r.rec("keydown")
	} else {
		r.rec("keyup")
	}
	return nil
}
func (r *recInput) Close() error { return nil }

// 拖动时每秒几十个 move，其中只有最后一个位置有意义。合并掉中间的，
// 是"拖动不卡"和"点击不迟到"的前提。
func TestInputPumpCoalescesMouseMoves(t *testing.T) {
	rec := &recInput{}
	p := newDeskInputPump(rec)
	pump := p.(*deskInputPump)

	// 直接压队列，绕开消费协程的时序，验证合并规则本身。
	pump.mu.Lock()
	for i := 0; i < 100; i++ {
		pump.q = append(pump.q, deskInputOp{kind: deskOpMove, x: i, y: i})
		if len(pump.q) > 1 && pump.q[len(pump.q)-2].kind == deskOpMove {
			pump.q = append(pump.q[:len(pump.q)-2], pump.q[len(pump.q)-1])
		}
	}
	n := len(pump.q)
	last := pump.q[len(pump.q)-1]
	pump.mu.Unlock()
	_ = p.Close()

	if n != 1 {
		t.Fatalf("相邻移动应被合并成一个，实际留下 %d 个", n)
	}
	if last.x != 99 || last.y != 99 {
		t.Fatalf("合并后应保留**最后**一个位置，实际 (%d,%d)", last.x, last.y)
	}
}

// 点击必须落在把光标移到位的那次移动之后。合并绝不能把顺序打乱，
// 否则会出现"点在了上一个位置"这种最难查的 bug。
func TestInputPumpPreservesOrderAcrossButtons(t *testing.T) {
	rec := &recInput{}
	p := newDeskInputPump(rec)
	for i := 0; i < 20; i++ {
		_ = p.MouseMove(i, i)
	}
	_ = p.MouseButton(1, true)
	for i := 0; i < 20; i++ {
		_ = p.MouseMove(100+i, 100+i)
	}
	_ = p.MouseButton(1, false)
	_ = p.Close() // Close 会排干队列

	ops := rec.snapshot()
	if len(ops) < 3 {
		t.Fatalf("事件太少：%v", ops)
	}
	// 按钮事件必须一个不少，且 down 在 up 前面
	var di, ui = -1, -1
	for i, o := range ops {
		if o == "down" && di < 0 {
			di = i
		}
		if o == "up" {
			ui = i
		}
	}
	if di < 0 || ui < 0 || di > ui {
		t.Fatalf("按下/抬起必须都在且保序，实际 %v", ops)
	}
	// down 之前必须至少有一次移动（光标要先到位）
	if di == 0 || ops[di-1] != "move" {
		t.Fatalf("按下之前应先有移动，实际 %v", ops)
	}
}

// 按键与按钮一个都不许丢——丢一个 keyup，远端那个键就一直按着。
func TestInputPumpNeverDropsKeysUnderPressure(t *testing.T) {
	rec := &recInput{}
	p := newDeskInputPump(rec)
	const keys = 50
	for i := 0; i < keys; i++ {
		for j := 0; j < 40; j++ { // 大量 move 冲刷队列
			_ = p.MouseMove(j, j)
		}
		_ = p.Key(65, true)
		_ = p.Key(65, false)
	}
	_ = p.Close()

	ops := rec.snapshot()
	down, up := 0, 0
	for _, o := range ops {
		switch o {
		case "keydown":
			down++
		case "keyup":
			up++
		}
	}
	if down != keys || up != keys {
		t.Fatalf("按键被丢了：keydown=%d keyup=%d（都应为 %d）", down, up, keys)
	}
}

// 读循环不能被注入拖住：底层每次注入 20ms，压 100 个事件也必须立刻返回。
func TestInputPumpDoesNotBlockCaller(t *testing.T) {
	rec := &recInput{delay: 20 * time.Millisecond}
	p := newDeskInputPump(rec)
	t.Cleanup(func() { _ = p.Close() })

	start := time.Now()
	for i := 0; i < 100; i++ {
		_ = p.MouseMove(i, i)
	}
	if el := time.Since(start); el > 300*time.Millisecond {
		t.Fatalf("入队阻塞了调用方 %v —— 读循环会被注入拖死", el)
	}
}

// Close 必须把队列排干：收尾时丢掉一个 keyup，用户那边就是一个按住不放的键。
func TestInputPumpDrainsOnClose(t *testing.T) {
	rec := &recInput{delay: time.Millisecond}
	p := newDeskInputPump(rec)
	for i := 0; i < 30; i++ {
		_ = p.Key(65, true)
		_ = p.Key(65, false)
	}
	_ = p.Close()
	if n := len(rec.snapshot()); n != 60 {
		t.Fatalf("Close 应排干队列，实际只注入了 %d/60 个事件", n)
	}
}
