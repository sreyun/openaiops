package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// isDeskCaptureFatal reports capture errors that will not self-heal with retries
// (Session 0, missing interactive desktop, …). Transient BitBlt / attach failures
// during desktop switches are NOT fatal and keep the existing capFails budget.
func isDeskCaptureFatal(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Session 0") ||
		strings.Contains(msg, "GetDC/GetWindowDC failed") ||
		strings.Contains(msg, "cannot read screen size") ||
		strings.Contains(msg, "screen capture unavailable") ||
		strings.Contains(msg, "desk_perm_denied") ||
		strings.Contains(msg, "Screen Recording")
}

// Agent-side web desktop channel (screen stream + input + file xfer).
// Mirrors terminal reverse channel: wait → rx + tx.

const deskSessionIdle = 2 * time.Hour
const deskSessionHard = 8 * time.Hour

type deskCapture interface {
	Size() (w, h int)
	Capture() (image.Image, error)
	Close() error
	Monitors() []deskMonitorInfo
	SetMonitor(id int) error
}

// Optional: capture reports its current monitor origin in virtual-screen coords
// so input can convert image-local clicks to absolute SetCursorPos targets.
type deskOriginAware interface {
	Origin() (x, y int)
}
type deskOriginSink interface {
	SetOrigin(x, y int)
}

func syncDeskOrigin(cap deskCapture, inp deskInput) {
	g, okG := cap.(deskOriginAware)
	s, okS := inp.(deskOriginSink)
	if okG && okS {
		s.SetOrigin(g.Origin())
	}
}

type deskMonitorInfo struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Primary bool   `json:"primary"`
}

type deskInput interface {
	MouseMove(x, y int) error
	MouseButton(button int, down bool) error
	MouseWheel(delta int) error
	Key(vk int, down bool) error
	Close() error
}

type deskQuality struct {
	Scale        float64  `json:"scale"`
	Quality      int      `json:"quality"`
	FPS          int      `json:"fps"`
	Codec        string   `json:"codec"` // jpeg | h264 | h265
	Monitor      int      `json:"monitor"`
	ClientW      int      `json:"client_w,omitempty"`
	ClientH      int      `json:"client_h,omitempty"`
	DPR          float64  `json:"dpr,omitempty"`
	Sharpness    float64  `json:"sharpness,omitempty"`
	AutoScale    bool     `json:"auto_scale,omitempty"`
	ClientCodecs []string `json:"client_codecs,omitempty"` // browser MSE capabilities
}

func defaultDeskQuality() deskQuality {
	// Prefer client-driven auto scale; until the browser reports viewport, use a
	// sharp-enough default (not the old soft 0.75 that looked blurry when upscaled).
	q := deskQuality{Scale: 0.9, Quality: 80, FPS: 20, Codec: "jpeg", Sharpness: 1.2, AutoScale: true}
	if deskLegacyCaptureHost() {
		q.Scale = 0.7
		q.Quality = 68
		q.FPS = 12
		q.Sharpness = 1.0
	}
	return q
}

// encodeScale picks the JPEG/H.264 downscale factor.
// When the browser sends client_w/h, we match the remote framebuffer to the
// visible stage (× DPR × sharpness) so 4K hosts aren't forced through a fixed
// 0.7× blur, and small viewports don't waste bandwidth on unused pixels.
func (q deskQuality) encodeScale(sw, sh int) float64 {
	if sw < 8 {
		sw = 8
	}
	scale := q.Scale
	if scale <= 0 {
		scale = 0.85
	}
	if q.AutoScale && q.ClientW >= 160 {
		sharp := q.Sharpness
		if sharp <= 0 {
			sharp = 1.15
		}
		if sharp > 2 {
			sharp = 2
		}
		dpr := q.DPR
		if dpr < 1 {
			dpr = 1
		}
		if dpr > 2.5 {
			dpr = 2.5
		}
		// Cap physical encode width so Retina 2× doesn't explode bandwidth.
		targetW := float64(q.ClientW) * dpr * sharp
		maxW := float64(q.ClientW) * 2.0 * sharp
		if targetW > maxW {
			targetW = maxW
		}
		auto := targetW / float64(sw)
		if auto > 1 {
			auto = 1
		}
		if auto < 0.4 {
			auto = 0.4
		}
		scale = auto
		// Also respect height so ultrawide stages don't over-encode height.
		if q.ClientH >= 120 {
			targetH := float64(q.ClientH) * dpr * sharp
			if ah := targetH / float64(maxInt(sh, 8)); ah > 0 && ah < scale {
				if ah < 0.4 {
					ah = 0.4
				}
				scale = ah
			}
		}
	}
	if scale > 1 {
		scale = 1
	}
	if scale < 0.25 {
		scale = 0.25
	}
	return scale
}

func (a *Agent) runDesktopChannelFor(t *serverTarget) {
	if a.identity.Fingerprint == "" {
		slog.Warn("远程桌面通道未启用：未采集到机器指纹", "server", t.server)
		return
	}
	slog.Info("远程桌面通道已就绪，等待服务端呼叫…", "server", t.server)
	backoff := newBackoffTimer(1*time.Second, 60*time.Second)
	for {
		// Desktop workers don't register; the service may reconcile the canonical
		// host id after we started. Re-read before every wait so we never sit on
		// a stale id for the process lifetime.
		if a.stateFile != "" {
			if id := readHostIDFromState(a.stateFile); id != "" && id != a.identity.HostID {
				slog.Info("桌面通道刷新 HostID", "old", short(a.identity.HostID), "new", short(id))
				a.identity.HostID = id
			}
		}
		// The state file holds ONE id, but each panel may know this machine by a
		// different one (see serverTarget.hostIDOr) — and the desktop worker is a
		// separate process that never registered, so it has no per-target id yet.
		// A fingerprint rejoin is idempotent (install-token counters only advance
		// for genuinely new hosts), making this the cheapest way to learn the id
		// this particular panel expects in deskWait. Skipped once registered.
		if !t.isRegistered() {
			_ = t.register(a.identity)
		}
		sid, lang, ok := a.deskWait(t)
		if !ok {
			d := backoff.next()
			time.Sleep(d)
			continue
		}
		backoff.reset()
		if sid == "" {
			continue
		}
		go a.runDesktopSession(t.server, sid, lang)
	}
}

func (a *Agent) deskWait(t *serverTarget) (sessionID, lang string, ok bool) {
	server := t.server
	q := url.Values{"host": {t.hostIDOr(a.identity.HostID)}}
	resp, err := agentGet(termWaitHTTP, server+"/api/v1/agent/desktop/wait?"+q.Encode(), a.identity.Fingerprint)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var out struct {
		Session string `json:"session"`
		Lang    string `json:"lang"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Session, out.Lang, true
}

func deskTxFrame(typ byte, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	copy(b[5:], payload)
	return b
}

// streamTxHTTP is a dedicated client for the *continuous* agent→server upload
// streams (desktop/terminal tx). The shared termHTTP buffers request-body writes
// in a 4KB bufio.Writer that only flushes when full — fine for report POSTs and
// for write-then-close streams (exec output, deskSendError), but fatal for a
// long-lived stream of small frames: the first meta ('S' ~500B) and low-detail
// JPEG/H264 frames sat in the buffer forever, so the browser reached "agent已接入"
// (tx headers arrived) but never got a single frame. WriteBufferSize=1 makes
// every frame (≥5B header) exceed the buffer and go straight to the socket.
var (
	streamTxOnce sync.Once
	streamTxHTTP *http.Client
)

func deskStreamClient() *http.Client {
	streamTxOnce.Do(func() {
		var tr *http.Transport
		if base, ok := termHTTP.Transport.(*http.Transport); ok && base != nil {
			tr = base.Clone() // inherit TLS/CA/proxy config applied at startup
		} else {
			tr = &http.Transport{
				Proxy:             http.ProxyFromEnvironment,
				ForceAttemptHTTP2: false,
			}
		}
		tr.WriteBufferSize = 1
		tr.ForceAttemptHTTP2 = false
		streamTxHTTP = &http.Client{
			Transport:     tr,
			CheckRedirect: preserveAgentRedirect,
		}
	})
	return streamTxHTTP
}

func (a *Agent) runDesktopSession(server, sid, lang string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("桌面会话异常已恢复", "session", sid, "panic", r)
		}
	}()

	cap, err := openDeskCapture()
	if err != nil {
		slog.Warn("桌面抓屏不可用", "session", sid, "err", err)
		a.deskSendError(server, sid, err.Error())
		return
	}
	defer cap.Close()

	inp, err := openDeskInput()
	viewOnly := false
	if err != nil {
		slog.Warn("桌面键鼠注入不可用，将以只读画面模式继续", "session", sid, "err", err)
		inp = &noopDeskInput{}
		viewOnly = true
	}
	defer inp.Close()

	slog.Info("远程桌面会话开始", "session", sid)
	var once sync.Once
	var stop atomic.Bool
	closeAll := func() {
		once.Do(func() {
			stop.Store(true)
			_ = cap.Close()
			_ = inp.Close()
		})
	}
	defer closeAll()

	q := defaultDeskQuality()
	var qMu sync.Mutex
	fileTxChan := make(chan []byte, 512)
	lastActive := time.Now()
	var actMu sync.Mutex
	touch := func() {
		actMu.Lock()
		lastActive = time.Now()
		actMu.Unlock()
	}

	hardTimer := time.AfterFunc(deskSessionHard, closeAll)
	defer hardTimer.Stop()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for !stop.Load() {
			<-t.C
			actMu.Lock()
			idle := time.Since(lastActive)
			actMu.Unlock()
			if idle > deskSessionIdle {
				closeAll()
				return
			}
		}
	}()

	pr, pw := io.Pipe()
	var pwMu sync.Mutex
	writeTx := func(b []byte) error {
		pwMu.Lock()
		defer pwMu.Unlock()
		_, err := pw.Write(b)
		if err == nil && len(b) > 0 && (b[0] == 'K' || b[0] == 'H' || b[0] == 'S') {
			// Video / meta traffic counts as activity so view-only monitoring
			// sessions are not torn down by the idle watchdog.
			touch()
		}
		return err
	}
	reqDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequest("POST", server+"/api/v1/agent/desktop/tx?session="+sid, pr)
		if err != nil {
			pw.Close()
			reqDone <- err
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		resp, doErr := deskStreamClient().Do(req)
		if doErr == nil {
			resp.Body.Close()
		}
		reqDone <- doErr
	}()

	sw, sh := cap.Size()
	mons := cap.Monitors()
	clipOK := deskClipboardSupported()
	// Resolve preferred codec before h264OK: on macOS prefer may lazily probe
	// avfoundation once (ffmpeg -list_devices). Calling deskH264Usable first
	// used to return false forever when probe was deferred.
	prefer := deskPreferredCodec()
	if v := deskPreferredVideoCodec(); v != "" {
		// Hardware HEVC / better encoder catalog can upgrade platform prefer.
		prefer = v
	}
	h264OK := deskH264Usable() || len(deskH264Encoders()) > 0
	h265OK := deskHEVCUsable()
	if (prefer == "h264" || prefer == "h265") && (h264OK || h265OK) && (q.Codec == "" || q.Codec == "jpeg") {
		q.Codec = prefer
	}
	codecs := []string{"jpeg"}
	if h264OK {
		codecs = append(codecs, "h264")
	}
	if h265OK {
		codecs = append(codecs, "h265")
	}
	encNames := []string{}
	for _, e := range deskH264Encoders() {
		encNames = append(encNames, e.Name)
	}
	for _, e := range deskH265Encoders() {
		encNames = append(encNames, e.Name)
	}
	metaMap := map[string]any{
		"w": sw, "h": sh, "os": runtimeGOOS(),
		"scale": q.Scale, "quality": q.Quality, "fps": q.FPS,
		"codec": q.Codec, "codecs": codecs, "prefer": prefer,
		"h264": h264OK, "h265": h265OK, "encoders": encNames,
		"clipboard": clipOK, "monitors": mons,
		"view_only": viewOnly,
	}
	for k, v := range deskMetaExtras(inp, viewOnly) {
		metaMap[k] = v
	}
	feats, _ := metaMap["features"].(map[string]bool)
	if feats == nil {
		feats = map[string]bool{}
	}
	feats["clipboard"] = clipOK
	feats["h264"] = h264OK
	feats["h265"] = h265OK
	feats["dnd"] = true
	feats["monitors"] = true
	metaMap["features"] = feats
	meta, _ := json.Marshal(metaMap)
	if err := writeTx(deskTxFrame('S', meta)); err != nil {
		pw.Close()
		<-reqDone
		return
	}
	if viewOnly {
		warn, _ := json.Marshal(map[string]string{
			"error": "键鼠注入不可用，当前为只读画面（Windows：确认以服务方式安装并已派生桌面 worker；Linux：安装 xdotool/ydotool；macOS：授予辅助功能权限或安装 cliclick）",
			"level": "warn",
		})
		_ = writeTx(deskTxFrame('E', warn))
	}

	var h264Mu sync.Mutex
	var h264 *h264Pipe
	var h264Scale float64
	var h264FPS int
	var h264MonID int
	var h264Qual int
	var h264Codec string
	var h264JPEGAt time.Time // occasional JPEG while on video for session replay
	stopH264 := func() {
		h264Mu.Lock()
		if h264 != nil {
			_ = h264.Close()
			h264 = nil
		}
		h264Scale, h264FPS, h264MonID, h264Qual, h264Codec = 0, 0, 0, 0, ""
		h264Mu.Unlock()
	}
	defer stopH264()

	// currentMon is read by the encoder goroutine and written by applyMonitor
	// (rx goroutine); guard it to avoid a data race on monitor switch.
	var monMu sync.Mutex
	currentMon := deskMonitorInfo{ID: 1, Width: sw, Height: sh, Primary: true}
	if len(mons) > 0 {
		currentMon = mons[0]
		for _, m := range mons {
			if m.Primary {
				currentMon = m
				break
			}
		}
	}

	// Encoder / capture loop (JPEG or H264)
	go func() {
		defer closeAll()
		defer pw.Close()
		// Desktop switches (lock↔unlock, UAC secure desktop, fast-user-switch)
		// make GDI BitBlt transiently fail for a frame or two while the worker
		// re-attaches to the new input desktop. Tearing the session down on the
		// first error would surface as a spurious "已断开". Tolerate a short burst
		// of consecutive failures (~4s) before giving up.
		capFails := 0
		const maxCapFails = 60
		// Blank-frame diagnostic: if capture SUCCEEDS but every frame is pure black
		// (a non-rendering target desktop — headless host, nobody logged in, or a
		// disconnected console), warn the operator ONCE with actionable guidance
		// instead of leaving them staring at an unexplained black screen.
		blankFrames := 0
		blankWarned := false
		const blankWarnAt = 40
		var deskMetaAt time.Time
		var lastFP uint64
		var sameFP int
		for !stop.Load() {
			actMu.Lock()
			inputHot := time.Since(lastActive) < 1800*time.Millisecond
			actMu.Unlock()
			qMu.Lock()
			cq := q
			qMu.Unlock()
			fps := cq.FPS
			if fps < 1 {
				fps = 1
			}
			if fps > 30 {
				fps = 30
			}
			secureNow := deskIsSecureName(deskCurrentDesktop(cap))
			// Lock / password / Update UI: prioritize responsiveness over bandwidth.
			if secureNow || inputHot {
				if fps < 22 {
					fps = 22
				}
				if fps > 28 {
					fps = 28
				}
			}
			interval := time.Second / time.Duration(fps)
			codec := deskNegotiateVideoCodec(cq.Codec, cq.ClientCodecs)
			if codec == "" {
				codec = "jpeg"
			}
			encScale := cq.encodeScale(sw, sh)
			vOpt := deskVideoOpts{
				Codec:         codec,
				Quality:       cq.Quality,
				FPS:           fps,
				Scale:         encScale,
				AllowSoftHEVC: codec == "h265",
			}

			if codec == "h264" || codec == "h265" {
				monMu.Lock()
				mon := currentMon
				monMu.Unlock()
				useRaw := deskNeedsRawH264()
				h264Mu.Lock()
				needRestart := h264 != nil && (h264Scale != encScale || h264FPS != fps || h264MonID != mon.ID ||
					h264.IsRaw() != useRaw || h264Qual != cq.Quality || h264Codec != codec)
				needStart := h264 == nil || needRestart
				h264Mu.Unlock()
				if needRestart {
					stopH264()
				}
				if needStart {
					var p *h264Pipe
					var err error
					if useRaw {
						rw := int(float64(mon.Width) * encScale)
						rh := int(float64(mon.Height) * encScale)
						p, err = startDeskVideoRawPipe(rw, rh, vOpt)
					} else {
						p, err = startDeskVideoPipe(mon, vOpt)
					}
					if err != nil {
						codec = "jpeg"
					} else {
						h264Mu.Lock()
						h264 = p
						h264Scale, h264FPS, h264MonID = encScale, fps, mon.ID
						h264Qual, h264Codec = cq.Quality, codec
						h264Mu.Unlock()
						go func(pipe *h264Pipe) {
							rbuf := make([]byte, 64*1024)
							for !stop.Load() {
								n, err := pipe.Read(rbuf)
								if n > 0 {
									chunk := make([]byte, n)
									copy(chunk, rbuf[:n])
									if writeTx(deskTxFrame('H', chunk)) != nil {
										return
									}
								}
								if err != nil {
									stopH264()
									return
								}
							}
						}(p)
					}
				}
				if (codec == "h264" || codec == "h265") && !useRaw {
					// gdigrab/x11grab/avfoundation owns capture; keep meta + sparse JPEG for replay.
					syncDeskOrigin(cap, inp)
					if nw, nh := cap.Size(); nw > 0 && nh > 0 && (nw != sw || nh != sh) {
						sw, sh = nw, nh
						js, _ := json.Marshal(map[string]any{"w": sw, "h": sh, "monitors": cap.Monitors()})
						_ = writeTx(deskTxFrame('S', js))
					}
					if time.Since(h264JPEGAt) > 2*time.Second {
						if img, err := cap.Capture(); err == nil {
							scaled := scaleImage(img, encScale)
							var jbuf bytes.Buffer
							if jpeg.Encode(&jbuf, scaled, &jpeg.Options{Quality: 40}) == nil && jbuf.Len() < 2<<20 {
								_ = writeTx(deskTxFrame('K', jbuf.Bytes()))
								h264JPEGAt = time.Now()
							}
						}
					}
					time.Sleep(interval)
					continue
				}
				// Raw video: fall through to Capture() then WriteFrame below.
			} else {
				stopH264()
			}

			img, err := cap.Capture()
			if err != nil {
				capFails++
				if !isDeskCaptureFatal(err) && capFails < maxCapFails {
					// Likely a desktop switch in progress; the next Capture()
					// re-attaches to the input desktop. Back off briefly and retry
					// instead of dropping the whole session.
					time.Sleep(interval)
					continue
				}
				msg, _ := json.Marshal(map[string]string{"error": err.Error()})
				_ = writeTx(deskTxFrame('E', msg))
				return
			}
			capFails = 0
			syncDeskOrigin(cap, inp)
			// Keep mouse mapping in sync when RDP/DPI resizes the desktop mid-session.
			if nw, nh := cap.Size(); nw > 0 && nh > 0 && (nw != sw || nh != sh) {
				sw, sh = nw, nh
				js, _ := json.Marshal(map[string]any{"w": sw, "h": sh, "monitors": cap.Monitors()})
				_ = writeTx(deskTxFrame('S', js))
			}
			// Push desktop name so UI can suppress false "solid frame" on Winlogon.
			if deskName := deskCurrentDesktop(cap); deskName != "" && time.Since(deskMetaAt) > 2*time.Second {
				deskMetaAt = time.Now()
				js, _ := json.Marshal(map[string]any{
					"desktop": deskName, "input_desktop_ok": true,
					"secure_desktop": deskIsSecureName(deskName),
					"lock_hint":      deskLockHintForDesktop(deskName),
				})
				_ = writeTx(deskTxFrame('S', js))
			}

			// Raw path H.264/H.265: feed capture frames into ffmpeg stdin.
			if (codec == "h264" || codec == "h265") && deskNeedsRawH264() {
				h264Mu.Lock()
				pipe := h264
				h264Mu.Unlock()
				if pipe != nil && pipe.IsRaw() {
					if err := pipe.WriteFrame(img); err != nil {
						stopH264()
					} else if time.Since(h264JPEGAt) > 2*time.Second {
						scaled := scaleImage(img, mathMin(cq.Scale, 0.5))
						var jbuf bytes.Buffer
						if jpeg.Encode(&jbuf, scaled, &jpeg.Options{Quality: 40}) == nil && jbuf.Len() < 2<<20 {
							_ = writeTx(deskTxFrame('K', jbuf.Bytes()))
							h264JPEGAt = time.Now()
						}
					}
					time.Sleep(interval)
					continue
				}
			}

			if !blankWarned {
				// Dark Winlogon / lock UI is often near-uniform; don't scare the operator
				// when we are correctly attached to the secure desktop — UI shows lock_hint.
				deskName := deskCurrentDesktop(cap)
				score := deskContentScore(img)
				uniform := isLikelyUniform(img, false)
				if deskIsSecureName(deskName) {
					blankFrames = 0
				} else if uniform && score < 8 {
					if blankFrames++; blankFrames >= blankWarnAt {
						blankWarned = true
						msg, _ := json.Marshal(map[string]string{
							"error": deskBlankFrameHint(),
							"level": "warn",
						})
						_ = writeTx(deskTxFrame('E', msg))
					}
				} else {
					blankFrames = 0
				}
			}
			// Idle desktops: skip identical frames. Lock/password UI must never
			// stall — sparse fingerprints miss password-dot updates, so encode
			// every tick while the operator is typing or on a secure desktop.
			if inputHot || secureNow {
				lastFP = 0
				sameFP = 0
			} else {
				fp := deskFrameFingerprint(img)
				if fp != 0 && fp == lastFP {
					sameFP++
					maxSkip := maxInt(1, cq.FPS) // ~1s idle heartbeat
					if sameFP <= maxSkip {
						time.Sleep(interval)
						continue
					}
					sameFP = 0
				} else {
					lastFP = fp
					sameFP = 0
				}
			}
			// Quality: raise when typing/unlocking so password dots stay crisp.
			encQual := cq.Quality
			if inputHot || secureNow {
				if encQual < 82 {
					encQual = 82
				}
			}
			if encQual < 20 {
				encQual = 20
			}
			if encQual > 95 {
				encQual = 95
			}
			// Prefer the viewport-matched scale; only crush further on huge frames
			// when the browser has NOT reported a client size (legacy clients).
			useScale := encScale
			if !cq.AutoScale || cq.ClientW < 160 {
				bounds := img.Bounds()
				estPixels := float64(bounds.Dx()) * float64(bounds.Dy()) * useScale * useScale
				if estPixels > 3_500_000 && useScale > 0.65 {
					useScale = 0.65
				} else if estPixels > 2_800_000 && useScale > 0.8 {
					useScale = 0.8
				}
			}
			scaled := scaleImage(img, useScale)
			var jbuf bytes.Buffer
			if err := jpeg.Encode(&jbuf, scaled, &jpeg.Options{Quality: encQual}); err != nil {
				time.Sleep(interval)
				continue
			}
			jpegBytes := jbuf.Bytes()
			// Soft cap: allow larger frames during interactive unlock so we don't
			// re-encode 4× and add multi-hundred-ms lag after each keystroke.
			softMax := 1400 << 10 // 1.4 MiB
			if inputHot || secureNow {
				softMax = 1800 << 10
			}
			const hardMax = 4 << 20
			if len(jpegBytes) > softMax {
				attempts := []struct {
					scale float64
					qual  int
				}{
					{useScale * 0.9, encQual},
					{useScale * 0.75, 70},
					{0.55, 58},
					{0.4, 48},
				}
				// Interactive unlock: only gentle shrink — never crush to mush.
				if inputHot || secureNow {
					attempts = []struct {
						scale float64
						qual  int
					}{
						{useScale * 0.95, encQual},
						{useScale * 0.88, maxInt(encQual-4, 78)},
					}
				}
				for _, attempt := range attempts {
					aq := attempt.qual
					if !(inputHot || secureNow) {
						if aq > 78 {
							aq = 78
						}
					}
					if aq < 36 {
						aq = 36
					}
					sc := attempt.scale
					if sc < 0.3 {
						sc = 0.3
					}
					alt := scaleImage(img, sc)
					var altBuf bytes.Buffer
					if err := jpeg.Encode(&altBuf, alt, &jpeg.Options{Quality: aq}); err != nil {
						continue
					}
					if altBuf.Len() > 0 && altBuf.Len() < len(jpegBytes) {
						jpegBytes = altBuf.Bytes()
					}
					if len(jpegBytes) <= softMax {
						break
					}
				}
			}
			if len(jpegBytes) > hardMax {
				time.Sleep(interval)
				continue
			}
			if err := writeTx(deskTxFrame('K', jpegBytes)); err != nil {
				return
			}
			time.Sleep(interval)
		}
	}()

	go func() {
		for !stop.Load() {
			select {
			case fr := <-fileTxChan:
				if err := writeTx(fr); err != nil {
					return
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()

	// Periodic clipboard push (agent → browser), every 2s when text changes
	if clipOK {
		go func() {
			var last string
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			const maxClip = 512 << 10 // 512 KiB — keep the tx stream healthy
			for !stop.Load() {
				<-t.C
				txt, err := deskClipboardGet()
				if err != nil || txt == last || txt == "" {
					continue
				}
				if len(txt) > maxClip {
					txt = txt[:maxClip]
				}
				last = txt
				js, _ := json.Marshal(map[string]string{"text": txt, "dir": "to_browser"})
				_ = writeTx(deskTxFrame('C', js))
			}
		}()
	}

	applyMonitor := func(id int) {
		if id <= 0 {
			return
		}
		_ = cap.SetMonitor(id)
		syncDeskOrigin(cap, inp)
		for _, m := range cap.Monitors() {
			if m.ID == id {
				monMu.Lock()
				currentMon = m
				monMu.Unlock()
				sw, sh = m.Width, m.Height
				break
			}
		}
		stopH264()
		js, _ := json.Marshal(map[string]any{"w": sw, "h": sh, "monitors": cap.Monitors(), "monitor": id})
		_ = writeTx(deskTxFrame('S', js))
	}
	syncDeskOrigin(cap, inp)

	// rx: input + files + clipboard + monitor
	go func() {
		defer closeAll()
		resp, err := agentGet(termHTTP, server+"/api/v1/agent/desktop/rx?session="+sid, a.identity.Fingerprint)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		dr := newDeadlineReader(resp.Body, 90*time.Second)
		readDeskFrames(dr, inp, lang, &q, &qMu, touch, fileTxChan, &sw, &sh, applyMonitor)
	}()

	<-reqDone
	closeAll()
	slog.Info("远程桌面会话结束", "session", sid)
}

// isLikelyBlank reports whether a captured frame is entirely (near) black. On
// Windows a successful BitBlt of a non-rendering desktop returns pure #000000, so
// an all-black frame indicates a dead/headless target session rather than a
// legitimately dark screen (which almost always has a non-black taskbar/wallpaper).
// Samples a sparse grid so the check is cheap.
func isLikelyBlank(img image.Image) bool {
	return isLikelyUniform(img, true)
}

// deskContentScore ranks how much "real UI" a frame has (0–100+). Pure black /
// flat fills score near 0; lock screens with faint CAD chrome still score > 0.
func deskContentScore(img image.Image) int {
	if img == nil {
		return 0
	}
	b := img.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 {
		return 0
	}
	const steps = 32
	sx := b.Dx() / steps
	sy := b.Dy() / steps
	if sx < 1 {
		sx = 1
	}
	if sy < 1 {
		sy = 1
	}
	var n, nonBlack, bright, edges int
	var prevR, prevG, prevB int = -1, -1, -1
	var minL, maxL = 255, 0
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r, g, bl := int(r16>>8), int(g16>>8), int(b16>>8)
			lum := (r*3 + g*6 + bl) / 10
			if lum < minL {
				minL = lum
			}
			if lum > maxL {
				maxL = lum
			}
			if lum > 12 {
				nonBlack++
			}
			if lum > 40 {
				bright++
			}
			if prevR >= 0 {
				d := absInt(r-prevR) + absInt(g-prevG) + absInt(bl-prevB)
				if d > 28 {
					edges++
				}
			}
			prevR, prevG, prevB = r, g, bl
			n++
		}
	}
	if n == 0 {
		return 0
	}
	// Weighted: edges (UI chrome) matter most; luminance range catches dark lock UI.
	score := edges*4 + bright*2 + nonBlack + (maxL - minL)
	return score
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// isLikelyUniform reports near-solid frames (any color). BitBlt of a disconnected
// console / wrong desktop often yields a flat blue or grey fill that is NOT black,
// so the old black-only check let those through as a "successful" stream.
// When blackOnly is true, only near-black fills match (legacy blank warning).
func isLikelyUniform(img image.Image, blackOnly bool) bool {
	b := img.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 {
		return false
	}
	const steps = 24
	sx := b.Dx() / steps
	sy := b.Dy() / steps
	if sx < 1 {
		sx = 1
	}
	if sy < 1 {
		sy = 1
	}
	var n int
	var sumR, sumG, sumB int64
	var minR, minG, minB uint32 = 255, 255, 255
	var maxR, maxG, maxB uint32
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r, g, bl := r16>>8, g16>>8, b16>>8
			sumR += int64(r)
			sumG += int64(g)
			sumB += int64(bl)
			if r < minR {
				minR = r
			}
			if g < minG {
				minG = g
			}
			if bl < minB {
				minB = bl
			}
			if r > maxR {
				maxR = r
			}
			if g > maxG {
				maxG = g
			}
			if bl > maxB {
				maxB = bl
			}
			n++
		}
	}
	if n < 8 {
		return false
	}
	// Range across samples is tiny → solid fill.
	if maxR-minR <= 6 && maxG-minG <= 6 && maxB-minB <= 6 {
		if blackOnly {
			return maxR <= 8 && maxG <= 8 && maxB <= 8
		}
		return true
	}
	return false
}

func (a *Agent) deskSendError(server, sid, msg string) {
	pr, pw := io.Pipe()
	go func() {
		js, _ := json.Marshal(map[string]string{"error": msg})
		_, _ = pw.Write(deskTxFrame('E', js))
		_ = pw.Close()
	}()
	req, err := http.NewRequest("POST", server+"/api/v1/agent/desktop/tx?session="+sid, pr)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
	if resp, err := termHTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

func runtimeGOOS() string {
	return deskGOOS()
}

// noopDeskInput keeps the session streaming when OS input tools are missing.
type noopDeskInput struct{}

func (noopDeskInput) MouseMove(x, y int) error                { return nil }
func (noopDeskInput) MouseButton(button int, down bool) error { return nil }
func (noopDeskInput) MouseWheel(delta int) error              { return nil }
func (noopDeskInput) Key(vk int, down bool) error             { return nil }
func (noopDeskInput) Close() error                            { return nil }

func scaleImage(src image.Image, scale float64) image.Image {
	if scale <= 0 || scale >= 0.99 {
		return src
	}
	b := src.Bounds()
	nw := int(float64(b.Dx()) * scale)
	nh := int(float64(b.Dy()) * scale)
	if nw < 8 || nh < 8 {
		return src
	}
	return scaleImageNN(src, nw, nh)
}

// scaleImageNN is a fast nearest-neighbor scaler. The previous At/Set path was
// ~10–30× slower on large Server desktops and dominated CPU before JPEG encode.
func scaleImageNN(src image.Image, nw, nh int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	rgba, ok := src.(*image.RGBA)
	if !ok {
		// One conversion then sample — still far cheaper than per-pixel At/Set.
		tmp := image.NewRGBA(image.Rect(0, 0, sw, sh))
		draw.Draw(tmp, tmp.Bounds(), src, b.Min, draw.Src)
		rgba = tmp
	}
	srcPix := rgba.Pix
	srcStride := rgba.Stride
	dstPix := dst.Pix
	dstStride := dst.Stride
	xOff := rgba.Rect.Min.X
	yOff := rgba.Rect.Min.Y
	for y := 0; y < nh; y++ {
		sy := yOff + y*sh/nh
		srcRow := (sy - rgba.Rect.Min.Y) * srcStride
		dstRow := y * dstStride
		for x := 0; x < nw; x++ {
			sx := xOff + x*sw/nw
			si := srcRow + (sx-rgba.Rect.Min.X)*4
			di := dstRow + x*4
			copy(dstPix[di:di+4], srcPix[si:si+4])
		}
	}
	return dst
}

// deskFrameFingerprint samples a sparse grid so identical / near-static frames
// (Update UI spinner pauses, idle desktop) skip a full JPEG encode+tx cycle.
func deskFrameFingerprint(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 8 || h < 8 {
		return 0
	}
	var h64 uint64 = 14695981039346656037
	const prime uint64 = 1099511628211
	const grid = 16
	rgba, ok := img.(*image.RGBA)
	for gy := 0; gy < grid; gy++ {
		y := b.Min.Y + (gy*h)/grid
		for gx := 0; gx < grid; gx++ {
			x := b.Min.X + (gx*w)/grid
			var r, g, bl, a uint32
			if ok {
				i := (y-rgba.Rect.Min.Y)*rgba.Stride + (x-rgba.Rect.Min.X)*4
				if i >= 0 && i+3 < len(rgba.Pix) {
					r = uint32(rgba.Pix[i])
					g = uint32(rgba.Pix[i+1])
					bl = uint32(rgba.Pix[i+2])
					a = uint32(rgba.Pix[i+3])
				}
			} else {
				r, g, bl, a = img.At(x, y).RGBA()
				r >>= 8
				g >>= 8
				bl >>= 8
				a >>= 8
			}
			h64 ^= uint64(r) | uint64(g)<<8 | uint64(bl)<<16 | uint64(a)<<24
			h64 *= prime
			h64 ^= uint64(x*131 + y)
			h64 *= prime
		}
	}
	return h64
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func derefInt(p *int, fallback int) int {
	if p == nil || *p <= 0 {
		return fallback
	}
	return *p
}

func readDeskFrames(r io.Reader, inp deskInput, lang string, q *deskQuality, qMu *sync.Mutex, touch func(), fileTxChan chan<- []byte, screenW, screenH *int, applyMonitor func(int)) {
	var hdr [3]byte
	type fileUploadState struct {
		file     *os.File
		filename string
		size     int64
		received int64
	}
	var upload *fileUploadState

	sendFileInfo := func(typ string, meta map[string]interface{}) {
		meta["type"] = typ
		js, _ := json.Marshal(meta)
		frame := deskTxFrame('F', js)
		select {
		case fileTxChan <- frame:
		case <-time.After(5 * time.Second):
		}
	}

	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if upload != nil {
				upload.file.Close()
				os.Remove(upload.file.Name())
			}
			return
		}
		n := int(binary.BigEndian.Uint16(hdr[1:]))
		payload := make([]byte, n)
		if n > 0 {
			if _, err := io.ReadFull(r, payload); err != nil {
				if upload != nil {
					upload.file.Close()
					os.Remove(upload.file.Name())
				}
				return
			}
		}
		touch()
		switch hdr[0] {
		case 'Q':
			var nq deskQuality
			if json.Unmarshal(payload, &nq) == nil {
				qMu.Lock()
				if nq.Scale > 0 {
					q.Scale = nq.Scale
				}
				if nq.Quality > 0 {
					q.Quality = nq.Quality
				}
				if nq.FPS > 0 {
					q.FPS = nq.FPS
				}
				if nq.ClientW > 0 {
					q.ClientW = nq.ClientW
				}
				if nq.ClientH > 0 {
					q.ClientH = nq.ClientH
				}
				if nq.DPR > 0 {
					q.DPR = nq.DPR
				}
				if nq.Sharpness > 0 {
					q.Sharpness = nq.Sharpness
				}
				// Presence of client_w enables auto scale even if the flag is omitted.
				if nq.AutoScale || nq.ClientW > 0 {
					q.AutoScale = true
				}
				if nq.Codec != "" {
					q.Codec = nq.Codec
				}
				if len(nq.ClientCodecs) > 0 {
					q.ClientCodecs = append([]string(nil), nq.ClientCodecs...)
				}
				if deskLegacyCaptureHost() {
					if q.FPS > 14 {
						q.FPS = 14
					}
					if q.Scale > 0.92 {
						q.Scale = 0.92
					}
					if q.Quality > 85 {
						q.Quality = 85
					}
					if q.Sharpness > 1.15 {
						q.Sharpness = 1.15
					}
				}
				applied := *q
				eff := applied.encodeScale(derefInt(screenW, 1920), derefInt(screenH, 1080))
				mon := nq.Monitor
				qMu.Unlock()
				if mon > 0 && applyMonitor != nil {
					applyMonitor(mon)
				}
				ack, _ := json.Marshal(map[string]any{
					"quality_ack":  true,
					"scale":        applied.Scale,
					"encode_scale": eff,
					"quality":      applied.Quality,
					"fps":          applied.FPS,
					"codec":        applied.Codec,
					"auto_scale":   applied.AutoScale,
					"client_w":     applied.ClientW,
					"client_h":     applied.ClientH,
					"sharpness":    applied.Sharpness,
				})
				select {
				case fileTxChan <- deskTxFrame('S', ack):
				default:
				}
			}
		case 'N':
			var ev struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(payload, &ev) == nil && applyMonitor != nil {
				applyMonitor(ev.ID)
			}
		case 'C':
			var ev struct {
				Text  string `json:"text"`
				Paste bool   `json:"paste"` // also inject Ctrl+V into focused control
			}
			if json.Unmarshal(payload, &ev) == nil && ev.Text != "" {
				txt := ev.Text
				if len(txt) > 512<<10 {
					txt = txt[:512<<10]
				}
				if ev.Paste {
					_ = deskDoPaste(inp, txt)
				} else {
					_ = deskClipboardSet(txt)
				}
			}
		case 'M':
			var ev struct {
				X      float64 `json:"x"`
				Y      float64 `json:"y"`
				Btn    int     `json:"btn"`
				Down   *bool   `json:"down"`
				Action string  `json:"action"`
				Norm   *bool   `json:"norm"` // true = [0,1] fractions; false/omit = pixel coords (modern UI)
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			sw, sh := 1920, 1080
			if screenW != nil && *screenW > 0 {
				sw = *screenW
			}
			if screenH != nil && *screenH > 0 {
				sh = *screenH
			}
			x := int(ev.X)
			y := int(ev.Y)
			useNorm := false
			if ev.Norm != nil {
				useNorm = *ev.Norm
			} else if sw > 2 && sh > 2 && ev.X >= 0 && ev.X <= 1 && ev.Y >= 0 && ev.Y <= 1 {
				// Legacy clients only: avoid treating pixel (0,0)/(1,1) as normalized.
				useNorm = (ev.X > 0 && ev.X < 1) || (ev.Y > 0 && ev.Y < 1)
			}
			if useNorm && sw > 0 {
				x = int(ev.X * float64(sw))
				y = int(ev.Y * float64(sh))
			}
			if sw > 0 {
				if x < 0 {
					x = 0
				} else if x >= sw {
					x = sw - 1
				}
			}
			if sh > 0 {
				if y < 0 {
					y = 0
				} else if y >= sh {
					y = sh - 1
				}
			}
			_ = inp.MouseMove(x, y)
			// Prefer Action; ignore Down when Action is set to avoid double button events.
			if ev.Action != "" {
				switch ev.Action {
				case "down":
					_ = inp.MouseButton(ev.Btn, true)
				case "up":
					_ = inp.MouseButton(ev.Btn, false)
				case "click":
					_ = inp.MouseButton(ev.Btn, true)
					_ = inp.MouseButton(ev.Btn, false)
				}
			} else if ev.Down != nil {
				_ = inp.MouseButton(ev.Btn, *ev.Down)
			}
		case 'W':
			var ev struct {
				Delta int `json:"delta"`
			}
			if json.Unmarshal(payload, &ev) == nil {
				_ = inp.MouseWheel(ev.Delta)
			}
		case 'B':
			var ev struct {
				Down  bool   `json:"down"`
				VK    int    `json:"vk"`
				Key   string `json:"key"`
				Code  string `json:"code"`
				Shift bool   `json:"shift"`
				Ctrl  bool   `json:"ctrl"`
				Alt   bool   `json:"alt"`
				Meta  bool   `json:"meta"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			// Printable chars (letters, digits, punctuation, CJK): UNICODE inject.
			// Fixes Shift+number / CapsLock / layout mismatch and the old bug that
			// mapped '$'→VK_HOME, '#'→VK_END. Skip when Ctrl/Alt/Meta (shortcuts).
			if !ev.Ctrl && !ev.Alt && !ev.Meta {
				if r, ok := deskPrintableRune(ev.Key); ok {
					if !ev.Down {
						continue // UNICODE path already sent down+up on keydown
					}
					if adv, ok := inp.(deskAdvancedInput); ok {
						_ = adv.TypeText(string(r))
						continue
					}
					// Platforms without TypeText: fall through to VK for A–Z/0–9 only.
				}
			}
			vk := ev.VK
			if vk == 0 {
				vk = deskKeyToVK(ev.Key, ev.Code)
			}
			if vk != 0 {
				_ = inp.Key(vk, ev.Down)
			}
		case 'A':
			sw0, sh0 := 0, 0
			if screenW != nil {
				sw0 = *screenW
			}
			if screenH != nil {
				sh0 = *screenH
			}
			handleDeskAction(inp, payload, sw0, sh0, fileTxChan)
		case 'f':
			var meta struct {
				Filename   string `json:"filename"`
				Size       int64  `json:"size"`
				TargetPath string `json:"target_path"`
			}
			if err := json.Unmarshal(payload, &meta); err != nil || meta.TargetPath == "" {
				continue
			}
			if meta.Size < 0 || meta.Size > 100<<20 {
				sendFileInfo("upload_ack", map[string]interface{}{
					"status": "error", "message": agentT(lang, "agent.file.upload_too_large"),
				})
				continue
			}
			target := filepath.Clean(meta.TargetPath)
			if !filepath.IsAbs(target) {
				target = filepath.Join(os.TempDir(), filepath.Base(target))
			}
			f, err := os.Create(target)
			if err != nil {
				sendFileInfo("upload_ack", map[string]interface{}{
					"status": "error", "message": agentT(lang, "agent.file.create_failed", err),
				})
				continue
			}
			upload = &fileUploadState{file: f, filename: meta.Filename, size: meta.Size}
		case 'u':
			if upload != nil {
				if upload.received+int64(len(payload)) > upload.size {
					upload.file.Close()
					os.Remove(upload.file.Name())
					sendFileInfo("upload_ack", map[string]interface{}{
						"status": "error", "message": agentT(lang, "agent.file.upload_oversize"),
					})
					upload = nil
					continue
				}
				if _, err := upload.file.Write(payload); err != nil {
					upload.file.Close()
					os.Remove(upload.file.Name())
					sendFileInfo("upload_ack", map[string]interface{}{
						"status": "error", "message": agentT(lang, "agent.file.write_failed", err),
					})
					upload = nil
					continue
				}
				upload.received += int64(len(payload))
			}
		case 'e':
			if upload != nil {
				upload.file.Close()
				sendFileInfo("upload_ack", map[string]interface{}{
					"status": "ok", "filename": upload.filename, "size": upload.received,
				})
				upload = nil
			}
		case 'd':
			var meta struct {
				RemotePath string `json:"remote_path"`
			}
			if json.Unmarshal(payload, &meta) == nil && meta.RemotePath != "" {
				go handleFileDownload(meta.RemotePath, lang, fileTxChan)
			}
		}
	}
}
