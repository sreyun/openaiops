package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/draw"
	"image/jpeg"
)

// 帧内差分（脏块）编码 —— 远程桌面"点一下要等三五秒"的真正解法。
//
// 在此之前，JPEG 通道每一帧都是**整屏重编码**：1920×1080 在 quality 80 下一帧
// 200–400KB，20fps 就是 4–8MB/s（32–64Mbps）。真实链路给不出这个带宽，于是多余的字节
// 堆在内核 socket 缓冲、反代缓冲和服务端队列里——**延迟就是这些缓冲区的深度**。
// 更糟的是打字时：inputHot 会关掉"整帧未变就跳过"的优化并把质量提到 82，于是每敲一个
// 键都触发一次整屏重编码。用户的感受精确地就是"输入延迟三五秒"。
//
// 真正的远程桌面（RDP / VNC）从来不这么干：它们只发**变化的那一小块**。在浏览器地址栏
// 里打一个字，屏幕上变的通常不到 1%，对应 2–4 个 128×128 的块、几 KB。同一条链路上，
// 延迟从"缓冲深度"塌缩回"一个 RTT"。
//
// 这里实现的就是这件事：
//   - 把缩放后的帧切成 128×128 的块，逐块算 CRC32（Castagnoli，amd64 上走 SSE4.2 指令，
//     整屏不到 1ms），与上一帧比对；
//   - 相邻的变化块**合并成矩形**（先横向连片，再纵向合并同跨度的行），减少 JPEG 头开销；
//   - 变化面积超过阈值、或矩形太碎时退回整帧——那时整帧的压缩率反而更好。
//
// 协议上新增 'T' 帧（见 encodeDeskTiles）。老浏览器不认识它，所以**必须由客户端先声明
// 支持**（'Q' 里的 tiles=true），Agent 才会启用；否则一律走原来的整帧 'K'。

const (
	// 128 是块大小的甜点：再小则 JPEG 头（每块约 0.6KB）占比过高，再大则一次改动
	// 会带上太多没变的像素。128 也是 16（JPEG MCU）的整数倍，块边不会出现半个 MCU。
	deskTileSize = 128
	// 变化面积超过整屏这个比例就整帧发：这时整帧 JPEG 的压缩率比一堆小块更好，
	// 而且省掉几十个 JPEG 头。
	deskTileFullRatio = 0.55
	// 单帧矩形数上限。碎片太多说明整屏都在动（视频、拖窗口），整帧更划算。
	deskTileMaxRects = 96
)

var deskTileCRCTable = crc32.MakeTable(crc32.Castagnoli)

// deskTiler 持有上一帧每个块的校验和。它只做"哪里变了"的判断，不关心编码。
type deskTiler struct {
	tile  int
	w, h  int
	cols  int
	rows  int
	sums  []uint32
	ready bool
}

func newDeskTiler() *deskTiler { return &deskTiler{tile: deskTileSize} }

// reset 丢弃历史：下一帧必然是整帧。
// 分辨率/缩放/显示器切换、客户端重连之后都要调用——客户端画布上的内容已经不是我们
// 以为的那一份了，再发差分就会把新旧像素混在一起。
func (t *deskTiler) reset() { t.ready = false }

// deskToRGBA 把任意 image.Image 归一成 *image.RGBA（零拷贝优先）。
// 块校验和与子图编码都要按行直接读 Pix，逐像素 At() 在 200 万像素上慢一个数量级。
func deskToRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// changed 返回相对上一帧变化的矩形（坐标以帧左上角为原点）。
// full=true 表示"别发差分了，整帧发"：几何变了、变化面积过大、或碎片过多。
func (t *deskTiler) changed(img *image.RGBA) (rects []image.Rectangle, full bool) {
	if img == nil {
		return nil, true
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, true
	}
	if t.tile <= 0 {
		t.tile = deskTileSize
	}
	cols := (w + t.tile - 1) / t.tile
	rows := (h + t.tile - 1) / t.tile
	if !t.ready || t.w != w || t.h != h || t.cols != cols || t.rows != rows {
		t.w, t.h, t.cols, t.rows = w, h, cols, rows
		t.sums = make([]uint32, cols*rows)
		t.hashInto(img, t.sums)
		t.ready = true
		return nil, true // 几何变了：客户端画布必须先拿到一整帧
	}

	next := make([]uint32, cols*rows)
	t.hashInto(img, next)
	dirty := make([]bool, cols*rows)
	changedTiles := 0
	for i := range next {
		if next[i] != t.sums[i] {
			dirty[i] = true
			changedTiles++
		}
	}
	t.sums = next
	if changedTiles == 0 {
		return nil, false
	}
	if float64(changedTiles)/float64(cols*rows) > deskTileFullRatio {
		return nil, true
	}
	rects = mergeDeskTiles(dirty, cols, rows, t.tile, w, h)
	if len(rects) > deskTileMaxRects {
		return nil, true
	}
	return rects, false
}

// hashInto 逐块算 CRC32。按行切片喂给 crc32.Update：amd64 上这是硬件指令，
// 整屏（约 8MB）不到 1ms，比后面的 JPEG 编码便宜一个数量级。
func (t *deskTiler) hashInto(img *image.RGBA, out []uint32) {
	b := img.Bounds()
	for ty := 0; ty < t.rows; ty++ {
		y0 := ty * t.tile
		y1 := minInt(y0+t.tile, t.h)
		for tx := 0; tx < t.cols; tx++ {
			x0 := tx * t.tile
			x1 := minInt(x0+t.tile, t.w)
			var crc uint32
			for y := y0; y < y1; y++ {
				off := img.PixOffset(b.Min.X+x0, b.Min.Y+y)
				crc = crc32.Update(crc, deskTileCRCTable, img.Pix[off:off+(x1-x0)*4])
			}
			out[ty*t.cols+tx] = crc
		}
	}
}

// mergeDeskTiles 把变化块合并成尽量少的矩形：先在每一行里把横向连片的块并成一条，
// 再把上下相邻、跨度完全相同的条并成一块。JPEG 每块有固定头开销，块数减半就是开销减半。
func mergeDeskTiles(dirty []bool, cols, rows, tile, w, h int) []image.Rectangle {
	type run struct{ x0, x1 int } // 以块为单位，[x0, x1)
	var out []image.Rectangle
	prev := map[run]image.Rectangle{}
	for ty := 0; ty < rows; ty++ {
		cur := map[run]image.Rectangle{}
		for tx := 0; tx < cols; {
			if !dirty[ty*cols+tx] {
				tx++
				continue
			}
			x0 := tx
			for tx < cols && dirty[ty*cols+tx] {
				tx++
			}
			r := run{x0, tx}
			px0 := x0 * tile
			px1 := minInt(tx*tile, w)
			py0 := ty * tile
			py1 := minInt((ty+1)*tile, h)
			if before, ok := prev[r]; ok {
				// 与上一行同跨度：纵向接上去
				before.Max.Y = py1
				cur[r] = before
				delete(prev, r)
				continue
			}
			cur[r] = image.Rect(px0, py0, px1, py1)
		}
		for _, rect := range prev { // 上一行没被接续的，收尾
			out = append(out, rect)
		}
		prev = cur
	}
	for _, rect := range prev {
		out = append(out, rect)
	}
	return out
}

// encodeDeskTiles 把变化矩形编成一个 'T' 帧的载荷。
//
//	u16 frameW | u16 frameH | u16 count
//	count × ( u16 x | u16 y | u16 w | u16 h | u32 len | len 字节 JPEG )
//
// 尺寸放在帧头里：客户端据此校验画布大小，对不上就等下一张整帧，绝不把差分画到
// 尺寸不同的画布上（那会画出错位的鬼影）。
func encodeDeskTiles(img *image.RGBA, rects []image.Rectangle, quality int) ([]byte, error) {
	b := img.Bounds()
	var buf bytes.Buffer
	var hdr [6]byte
	binary.BigEndian.PutUint16(hdr[0:], uint16(b.Dx()))
	binary.BigEndian.PutUint16(hdr[2:], uint16(b.Dy()))
	binary.BigEndian.PutUint16(hdr[4:], uint16(len(rects)))
	buf.Write(hdr[:])
	var jb bytes.Buffer
	for _, r := range rects {
		sub := img.SubImage(image.Rect(b.Min.X+r.Min.X, b.Min.Y+r.Min.Y, b.Min.X+r.Max.X, b.Min.Y+r.Max.Y))
		jb.Reset()
		if err := jpeg.Encode(&jb, sub, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		var rh [12]byte
		binary.BigEndian.PutUint16(rh[0:], uint16(r.Min.X))
		binary.BigEndian.PutUint16(rh[2:], uint16(r.Min.Y))
		binary.BigEndian.PutUint16(rh[4:], uint16(r.Dx()))
		binary.BigEndian.PutUint16(rh[6:], uint16(r.Dy()))
		binary.BigEndian.PutUint32(rh[8:], uint32(jb.Len()))
		buf.Write(rh[:])
		buf.Write(jb.Bytes())
	}
	return buf.Bytes(), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
