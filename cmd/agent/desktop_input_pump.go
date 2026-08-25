package main

import (
	"sync"
)

// 远程桌面输入泵：把注入从 WS 读循环上摘下来，并合并被后续位置作废的鼠标移动。
//
// 为什么必须有这一层：Windows 走的是进程内的原生 API（SendInput），一次注入是微秒级；
// 而 Linux / 麒麟 / macOS 走的是**外部命令**（xdotool / ydotool / wtype / cliclick），
// 每一个事件都要 fork+exec、动态链接、连一次 X11——一次十几到几十毫秒。
// 更要命的是这些调用原来直接跑在读循环上：
//
//	_ = inp.MouseMove(x, y)   // ← 阻塞读循环
//
// 于是拖动鼠标时每秒几十个 move 事件把循环彻底堵死，排在它们后面的**点击**要等
// 前面所有 move 一个个 exec 完才轮得到——用户看到的就是"拖动很卡、点了没反应"。
//
// 这一层做三件事，缺一不可：
//
//  1. **挪到独立协程**：读循环再也不会因为注入慢而停下来，事件该收就收。
//  2. **合并相邻的鼠标移动**：只有最后那个位置是有意义的，中间位置是纯粹的浪费。
//     这是所有远程桌面协议（RDP/VNC/SPICE）都在做的事。
//  3. **严格保序，且只丢 move**：点击必须落在把光标移到位的那次移动之后；
//     按键、滚轮、按钮**一个都不许丢、不许合并**——丢一个 keyup 就是按键卡住不放。
//
// 合并只发生在队尾且前一个也是 move 的时候，所以 move→click 之间不会被穿插打乱。
type deskInputPump struct {
	inp deskInput

	mu     sync.Mutex
	cond   *sync.Cond
	q      []deskInputOp
	closed bool
	done   chan struct{}
}

type deskInputOpKind uint8

const (
	deskOpMove deskInputOpKind = iota
	deskOpButton
	deskOpWheel
	deskOpKey
	deskOpText
	deskOpCAD
)

type deskInputOp struct {
	kind deskInputOpKind
	x, y int  // move
	code int  // button / vk / wheel delta
	down bool // button / key
	text string
}

// deskInputQueueMax 是队列上限。正常情况下队列长度是 0~2；到达上限说明注入端已经
// 严重落后（工具缺失、ydotoold 没起来、机器卡死）。这时候丢**最老的 move**——
// 它本来就已经被后面的位置作废了；按键与按钮永远不丢。
const deskInputQueueMax = 512

// newDeskInputPump 包一层输入泵。
//
// 返回类型分两种是**必须的**：Go 的接口断言是静态的，如果无论底层支持与否都返回带
// SendCAD/TypeText 的类型，那么 `inp.(deskAdvancedInput)` 就会在不支持的平台上也成立，
// 上层那条「没有 TypeText 就退回 VK 逐键发送」的兜底再也不会触发——中文输入会静默失效。
func newDeskInputPump(inp deskInput) deskInput {
	p := &deskInputPump{inp: inp, done: make(chan struct{})}
	p.cond = sync.NewCond(&p.mu)
	go p.run()
	if adv, ok := inp.(deskAdvancedInput); ok {
		return &deskInputPumpAdvanced{deskInputPump: p, adv: adv}
	}
	return p
}

// deskInputPumpAdvanced 只在底层确实实现了 deskAdvancedInput 时才被构造出来。
type deskInputPumpAdvanced struct {
	*deskInputPump
	adv deskAdvancedInput
}

// SendCAD 也走同一条队列：它本质上是一串按键，和普通按键抢注入工具会乱序。
func (p *deskInputPumpAdvanced) SendCAD() error {
	p.push(deskInputOp{kind: deskOpCAD})
	return nil
}

// DeskInputMeta 是纯查询（能力探测），不是事件，直接转发，不进队列。
func (p *deskInputPumpAdvanced) DeskInputMeta() deskInputMeta { return p.adv.DeskInputMeta() }

func (p *deskInputPump) run() {
	defer close(p.done)
	for {
		p.mu.Lock()
		for len(p.q) == 0 && !p.closed {
			p.cond.Wait()
		}
		if len(p.q) == 0 && p.closed {
			p.mu.Unlock()
			return
		}
		op := p.q[0]
		p.q = p.q[1:]
		p.mu.Unlock()

		switch op.kind {
		case deskOpMove:
			_ = p.inp.MouseMove(op.x, op.y)
		case deskOpButton:
			_ = p.inp.MouseButton(op.code, op.down)
		case deskOpWheel:
			_ = p.inp.MouseWheel(op.code)
		case deskOpKey:
			_ = p.inp.Key(op.code, op.down)
		case deskOpText:
			if adv, ok := p.inp.(deskAdvancedInput); ok {
				_ = adv.TypeText(op.text)
			}
		case deskOpCAD:
			if adv, ok := p.inp.(deskAdvancedInput); ok {
				_ = adv.SendCAD()
			}
		}
	}
}

func (p *deskInputPump) push(op deskInputOp) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	// 队尾也是移动 → 直接覆盖：中间位置已经被这一个作废了。
	if op.kind == deskOpMove && len(p.q) > 0 && p.q[len(p.q)-1].kind == deskOpMove {
		p.q[len(p.q)-1] = op
		p.cond.Signal()
		return
	}
	if len(p.q) >= deskInputQueueMax {
		// 只丢 move，且从最老的开始丢——它早就被后面的位置作废了。
		// 如果队列里一个 move 都没有（全是按键/按钮），那就让它超限：
		// 丢一个 keyup，远端那个键就一直按着，比队列长几百项严重得多。
		for i := range p.q {
			if p.q[i].kind == deskOpMove {
				p.q = append(p.q[:i], p.q[i+1:]...)
				break
			}
		}
	}
	p.q = append(p.q, op)
	p.cond.Signal()
}

func (p *deskInputPump) MouseMove(x, y int) error {
	p.push(deskInputOp{kind: deskOpMove, x: x, y: y})
	return nil
}

func (p *deskInputPump) MouseButton(button int, down bool) error {
	p.push(deskInputOp{kind: deskOpButton, code: button, down: down})
	return nil
}

func (p *deskInputPump) MouseWheel(delta int) error {
	p.push(deskInputOp{kind: deskOpWheel, code: delta})
	return nil
}

func (p *deskInputPump) Key(vk int, down bool) error {
	p.push(deskInputOp{kind: deskOpKey, code: vk, down: down})
	return nil
}

// TypeText 让 deskAdvancedInput 也走同一条队列——否则输入法/长文本粘贴会和
// 按键事件抢同一个注入工具，顺序错乱。
func (p *deskInputPump) TypeText(s string) error {
	p.push(deskInputOp{kind: deskOpText, text: s})
	return nil
}

// SetOrigin 透传给底层（多显示器裁剪原点）。它不是事件，直接转发即可。
func (p *deskInputPump) SetOrigin(x, y int) {
	if s, ok := p.inp.(deskOriginSink); ok {
		s.SetOrigin(x, y)
	}
}

func (p *deskInputPump) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	<-p.done // 排干队列：收尾时丢掉一个 keyup，远端就会一直按着那个键
	return p.inp.Close()
}
