//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"unsafe"
)

// Windows 侧的远程分辨率控制：EnumDisplaySettingsExW 列模式，ChangeDisplaySettingsExW 切换。
//
// 只动**主显示器**：多屏机器上把每块屏都改一遍既危险又没必要，操作员看的是当前那一块。
// 会话开始时记住原始模式，SetMode 之后由会话收尾无条件 Restore ——远程改了人家机器的
// 分辨率却不还回去，是这类工具最招人烦的行为之一。

var (
	procEnumDisplaySettingsExW  = modUser32.NewProc("EnumDisplaySettingsExW")
	procChangeDisplaySettingsEx = modUser32.NewProc("ChangeDisplaySettingsExW")
)

const (
	enumCurrentSettings  uint32 = 0xFFFFFFFF // ENUM_CURRENT_SETTINGS
	dmPelsWidth                 = 0x00080000
	dmPelsHeight                = 0x00100000
	dmDisplayFrequency          = 0x00400000
	cdsUpdateRegistry           = 0x00000001
	cdsTest                     = 0x00000002
	dispChangeSuccessful        = 0
	dispChangeRestart           = 1
	dispChangeBadMode           = -2
	dispChangeNotUpdated        = -3
	dispChangeBadFlags          = -4
	dispChangeBadParam          = -5
	dispChangeFailed            = -1
)

// devModeW 只声明到我们要用的字段为止，尾部按 Win32 DEVMODEW 的大小补齐。
type devModeW struct {
	DeviceName    [32]uint16
	SpecVersion   uint16
	DriverVersion uint16
	Size          uint16
	DriverExtra   uint16
	Fields        uint32
	// union: POINTL + 两个 DWORD（打印机用），显示模式下用不到
	Position           [2]int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

func newDevMode() devModeW {
	var dm devModeW
	dm.Size = uint16(unsafe.Sizeof(dm))
	return dm
}

// enumDisplayMode 读取主显示器的某个模式（idx = enumCurrentSettings 取当前值）。
func enumDisplayMode(idx uint32) (devModeW, bool) {
	dm := newDevMode()
	r, _, _ := procEnumDisplaySettingsExW.Call(0, uintptr(idx), uintptr(unsafe.Pointer(&dm)), 0)
	return dm, r != 0
}

func (c *winCapture) Modes() []deskDisplayMode {
	var out []deskDisplayMode
	for i := uint32(0); i < 4096; i++ {
		dm, ok := enumDisplayMode(i)
		if !ok {
			break
		}
		if dm.PelsWidth == 0 || dm.PelsHeight == 0 {
			continue
		}
		out = append(out, deskDisplayMode{
			W: int(dm.PelsWidth), H: int(dm.PelsHeight), Freq: int(dm.DisplayFrequency),
		})
	}
	return deskSortModes(out)
}

func (c *winCapture) SetMode(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("分辨率参数无效：%d×%d", w, h)
	}
	cur, ok := enumDisplayMode(enumCurrentSettings)
	if !ok {
		return fmt.Errorf("读不到当前显示模式")
	}
	if c.origMode == nil {
		saved := cur
		c.origMode = &saved
	}
	if int(cur.PelsWidth) == w && int(cur.PelsHeight) == h {
		return nil // 已经是目标分辨率
	}
	dm := newDevMode()
	dm.Fields = dmPelsWidth | dmPelsHeight
	dm.PelsWidth = uint32(w)
	dm.PelsHeight = uint32(h)
	// 保留当前刷新率：只改像素数，别把 144Hz 的屏按成 60Hz。
	if cur.DisplayFrequency > 1 {
		dm.Fields |= dmDisplayFrequency
		dm.DisplayFrequency = cur.DisplayFrequency
	}
	// 先 TEST：驱动不接受的模式在这一步就会被挡下，不会让屏幕黑一下再弹回来。
	if r, _, _ := procChangeDisplaySettingsEx.Call(0, uintptr(unsafe.Pointer(&dm)), 0, uintptr(cdsTest), 0); int32(r) != dispChangeSuccessful {
		return fmt.Errorf("显示驱动不接受 %d×%d（%s）", w, h, displayChangeError(int32(r)))
	}
	r, _, _ := procChangeDisplaySettingsEx.Call(0, uintptr(unsafe.Pointer(&dm)), 0, uintptr(cdsUpdateRegistry), 0)
	if int32(r) != dispChangeSuccessful {
		return fmt.Errorf("切换到 %d×%d 失败（%s）", w, h, displayChangeError(int32(r)))
	}
	slog.Info("远程桌面已切换分辨率", "w", w, "h", h,
		"from", fmt.Sprintf("%dx%d", cur.PelsWidth, cur.PelsHeight))
	return nil
}

func (c *winCapture) Restore() error {
	if c.origMode == nil {
		return nil
	}
	dm := *c.origMode
	c.origMode = nil
	dm.Fields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency
	dm.Size = uint16(unsafe.Sizeof(dm))
	r, _, _ := procChangeDisplaySettingsEx.Call(0, uintptr(unsafe.Pointer(&dm)), 0, uintptr(cdsUpdateRegistry), 0)
	if int32(r) != dispChangeSuccessful {
		return fmt.Errorf("恢复原分辨率失败（%s）", displayChangeError(int32(r)))
	}
	slog.Info("远程桌面已恢复原分辨率", "w", dm.PelsWidth, "h", dm.PelsHeight)
	return nil
}

func displayChangeError(code int32) string {
	switch code {
	case dispChangeRestart:
		return "需要重启才能生效"
	case dispChangeBadMode:
		return "不支持该模式"
	case dispChangeNotUpdated:
		return "无法写入注册表"
	case dispChangeBadFlags:
		return "参数标志错误"
	case dispChangeBadParam:
		return "参数错误"
	case dispChangeFailed:
		return "显示驱动拒绝"
	default:
		return fmt.Sprintf("错误码 %d", code)
	}
}
