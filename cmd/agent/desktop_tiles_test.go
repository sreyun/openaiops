package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// 脏块差分是"远程桌面点一下等三五秒"的正面解法：整屏 JPEG 一帧 200–400KB，20fps 就是
// 32–64Mbps，链路给不出来，多出来的字节全堆在缓冲里，那个深度就是用户感受到的延迟。
// 下面几条守的是这套差分的三件事：**该发的一块都不能漏**、**没变就一个字节都别发**、
// **判断错了要退回整帧**（宁可多花带宽，也不能把错位的像素画到用户屏幕上）。

func newTestFrame(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestDeskTilerFirstFrameIsFull(t *testing.T) {
	tiler := newDeskTiler()
	img := newTestFrame(512, 384, color.RGBA{20, 20, 20, 255})
	rects, full := tiler.changed(img)
	if !full || len(rects) != 0 {
		t.Fatalf("第一帧必须整帧发（客户端手里还什么都没有）：full=%v rects=%d", full, len(rects))
	}
}

func TestDeskTilerIdenticalFrameSendsNothing(t *testing.T) {
	tiler := newDeskTiler()
	img := newTestFrame(512, 384, color.RGBA{20, 20, 20, 255})
	tiler.changed(img) // 建立基线
	rects, full := tiler.changed(img)
	if full || len(rects) != 0 {
		t.Fatalf("画面没变就不该发任何东西：full=%v rects=%d", full, len(rects))
	}
}

func TestDeskTilerReportsOnlyChangedArea(t *testing.T) {
	tiler := newDeskTiler()
	img := newTestFrame(512, 384, color.RGBA{20, 20, 20, 255})
	tiler.changed(img)

	// 只改一个像素（模拟光标闪一下 / 地址栏多一个字符）。
	img.SetRGBA(300, 200, color.RGBA{255, 255, 255, 255})
	rects, full := tiler.changed(img)
	if full {
		t.Fatal("改一个像素不该退回整帧")
	}
	if len(rects) != 1 {
		t.Fatalf("应当只报一块，实际 %d 块：%v", len(rects), rects)
	}
	if !image.Pt(300, 200).In(rects[0]) {
		t.Fatalf("报出来的矩形没盖住变化点：%v", rects[0])
	}
	if area := rects[0].Dx() * rects[0].Dy(); area > deskTileSize*deskTileSize {
		t.Fatalf("一个像素的改动被放大成 %d 像素的矩形：%v", area, rects[0])
	}
}

func TestDeskTilerMergesAdjacentTiles(t *testing.T) {
	tiler := newDeskTiler()
	img := newTestFrame(512, 384, color.RGBA{20, 20, 20, 255})
	tiler.changed(img)

	// 横跨两个相邻块各改一个点：应当合成一条，而不是两块（每块 JPEG 头约 0.6KB）。
	img.SetRGBA(10, 10, color.RGBA{255, 0, 0, 255})
	img.SetRGBA(140, 10, color.RGBA{255, 0, 0, 255})
	rects, full := tiler.changed(img)
	if full {
		t.Fatal("两个块的改动不该退回整帧")
	}
	if len(rects) != 1 {
		t.Fatalf("相邻块应当合并成一个矩形，实际 %d 个：%v", len(rects), rects)
	}
	if rects[0].Dx() < 2*deskTileSize {
		t.Fatalf("合并后的矩形没有跨过两个块：%v", rects[0])
	}
}

func TestDeskTilerFallsBackToFullFrameOnBigChange(t *testing.T) {
	tiler := newDeskTiler()
	img := newTestFrame(512, 384, color.RGBA{20, 20, 20, 255})
	tiler.changed(img)

	// 整屏换色（切窗口 / 播放视频）：这时一堆小 JPEG 反而比一张整帧更费，退回整帧。
	for y := 0; y < 384; y++ {
		for x := 0; x < 512; x++ {
			img.SetRGBA(x, y, color.RGBA{200, 200, 200, 255})
		}
	}
	rects, full := tiler.changed(img)
	if !full {
		t.Fatalf("整屏都变了应当退回整帧，实际报了 %d 块", len(rects))
	}
}

func TestDeskTilerResetsOnGeometryChange(t *testing.T) {
	tiler := newDeskTiler()
	tiler.changed(newTestFrame(512, 384, color.RGBA{20, 20, 20, 255}))
	// 分辨率 / 缩放变了：客户端画布上的底图已经不是我们记的那一张，必须整帧重来。
	rects, full := tiler.changed(newTestFrame(640, 480, color.RGBA{20, 20, 20, 255}))
	if !full || len(rects) != 0 {
		t.Fatalf("几何变化必须整帧重来：full=%v rects=%d", full, len(rects))
	}
}

// encodeDeskTiles 的线格式要与浏览器解析器（protocol.ts 的 parseDeskTiles /
// desktop.js 的 drawDeskTiles）逐字节对齐，所以这里按协议手工解一遍。
func TestEncodeDeskTilesWireFormat(t *testing.T) {
	img := newTestFrame(512, 384, color.RGBA{10, 120, 220, 255})
	rects := []image.Rectangle{
		image.Rect(0, 0, 128, 128),
		image.Rect(256, 128, 384, 256),
	}
	payload, err := encodeDeskTiles(img, rects, 80)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if len(payload) < 6 {
		t.Fatal("载荷太短，连帧头都不够")
	}
	if w := binary.BigEndian.Uint16(payload[0:]); w != 512 {
		t.Fatalf("帧宽 = %d, want 512", w)
	}
	if h := binary.BigEndian.Uint16(payload[2:]); h != 384 {
		t.Fatalf("帧高 = %d, want 384", h)
	}
	count := int(binary.BigEndian.Uint16(payload[4:]))
	if count != len(rects) {
		t.Fatalf("块数 = %d, want %d", count, len(rects))
	}
	off := 6
	for i := 0; i < count; i++ {
		if off+12 > len(payload) {
			t.Fatalf("第 %d 块的头被截断了", i)
		}
		x := int(binary.BigEndian.Uint16(payload[off:]))
		y := int(binary.BigEndian.Uint16(payload[off+2:]))
		w := int(binary.BigEndian.Uint16(payload[off+4:]))
		h := int(binary.BigEndian.Uint16(payload[off+6:]))
		n := int(binary.BigEndian.Uint32(payload[off+8:]))
		off += 12
		if off+n > len(payload) {
			t.Fatalf("第 %d 块的 JPEG 被截断了", i)
		}
		if x != rects[i].Min.X || y != rects[i].Min.Y || w != rects[i].Dx() || h != rects[i].Dy() {
			t.Fatalf("第 %d 块坐标 = (%d,%d,%d,%d), want %v", i, x, y, w, h, rects[i])
		}
		// 每块都必须是一张能独立解码的 JPEG，尺寸正好等于矩形尺寸——
		// 浏览器就是照着 (x,y,w,h) 把它贴回画布的，尺寸对不上就会糊出错位。
		decoded, err := jpeg.Decode(bytes.NewReader(payload[off : off+n]))
		if err != nil {
			t.Fatalf("第 %d 块解码失败：%v", i, err)
		}
		if decoded.Bounds().Dx() != w || decoded.Bounds().Dy() != h {
			t.Fatalf("第 %d 块解码尺寸 = %v, want %dx%d", i, decoded.Bounds(), w, h)
		}
		off += n
	}
	if off != len(payload) {
		t.Fatalf("载荷尾部多出 %d 字节", len(payload)-off)
	}
}

// 差分的意义全在这条上：同一屏只改一个字符时，字节数必须比整帧小一个数量级。
// 这正是"点一下等三五秒"与"跟本地一样跟手"的分界线。
func TestDeskTilesAreFarSmallerThanFullFrame(t *testing.T) {
	img := newTestFrame(1280, 800, color.RGBA{30, 30, 30, 255})
	// 造一点纹理，免得整屏纯色让 JPEG 压得不真实。
	for y := 0; y < 800; y += 3 {
		for x := 0; x < 1280; x += 2 {
			img.SetRGBA(x, y, color.RGBA{uint8(x % 251), uint8(y % 241), 90, 255})
		}
	}
	tiler := newDeskTiler()
	tiler.changed(img)

	var full bytes.Buffer
	if err := jpeg.Encode(&full, img, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatalf("整帧编码失败：%v", err)
	}

	// 改动一个字符大小的区域。
	for y := 400; y < 416; y++ {
		for x := 600; x < 610; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	rects, isFull := tiler.changed(img)
	if isFull || len(rects) == 0 {
		t.Fatalf("一个字符的改动应当走差分：full=%v rects=%d", isFull, len(rects))
	}
	payload, err := encodeDeskTiles(img, rects, 82)
	if err != nil {
		t.Fatalf("差分编码失败：%v", err)
	}
	if len(payload)*8 > full.Len() {
		t.Fatalf("差分帧 %d 字节 vs 整帧 %d 字节——没有拉开数量级，延迟问题不会好转",
			len(payload), full.Len())
	}
	t.Logf("差分 %d 字节 / 整帧 %d 字节（%.1f%%）", len(payload), full.Len(),
		100*float64(len(payload))/float64(full.Len()))
}
