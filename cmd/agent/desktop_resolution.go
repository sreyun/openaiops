package main

import (
	"fmt"
	"sort"
)

// 远程分辨率自适应。
//
// 画面缩放（encodeScale）解决的是"把远端这块画布压到我这块窗口里"，可它压不出不存在的
// 像素：远端是 1024×768、本地窗口 2560×1400 时，只能把 1024×768 放大糊在屏幕上。
// 真正的远程桌面（mstsc/RDP）在这一步是**改远端的显示分辨率**去迁就客户端。
//
// 这里给的就是这条能力：会话里可以把远端切到最贴近本地窗口的显示模式，会话结束再自动
// 切回去。刻意做成**显式动作**而不是自动跟随：目标机往往就摆在某个人面前，或者接着一台
// 物理显示器，悄悄改人家的分辨率是不礼貌也不安全的。
//
// 平台支持见各自的实现文件：Windows 走 EnumDisplaySettings/ChangeDisplaySettingsEx，
// Linux 走 xrandr（有就用），其余平台明确报"不支持"，而不是假装成功。

// deskDisplayMode 是一个可用的显示模式。
type deskDisplayMode struct {
	W    int `json:"w"`
	H    int `json:"h"`
	Freq int `json:"freq,omitempty"`
}

// deskResolutionCtl 由具备改分辨率能力的平台实现。
type deskResolutionCtl interface {
	// Modes 返回当前显示器可用的模式（去重、按面积排序）。
	Modes() []deskDisplayMode
	// SetMode 切到指定分辨率；实现应记住原始模式以便 Restore。
	SetMode(w, h int) error
	// Restore 切回会话开始时的模式。没改过就是空操作。
	Restore() error
}

// deskPickMode 从可用模式里挑最贴近客户端窗口的一个。
//
// 规则：优先"不超过客户端、且长宽比最接近"的模式里面积最大的那个——放大糊掉的画面
// 比黑边更难受，但超出窗口就得二次缩小，等于白白多编码像素。都不满足时退回面积最接近的。
func deskPickMode(modes []deskDisplayMode, clientW, clientH int) (deskDisplayMode, bool) {
	if len(modes) == 0 || clientW < 320 || clientH < 240 {
		return deskDisplayMode{}, false
	}
	want := float64(clientW) / float64(clientH)
	best := deskDisplayMode{}
	bestScore := -1.0
	for _, m := range modes {
		if m.W < 640 || m.H < 480 {
			continue
		}
		ratio := float64(m.W) / float64(m.H)
		ratioPenalty := ratio/want + want/ratio - 2 // 0 = 完全一致
		fit := 1.0
		if m.W > clientW || m.H > clientH {
			fit = 0.35 // 超出窗口要二次缩小，扣分但不排除
		}
		area := float64(m.W*m.H) / float64(clientW*clientH)
		if area > 1 {
			area = 1 / area
		}
		score := fit * area / (1 + 4*ratioPenalty)
		if score > bestScore {
			bestScore, best = score, m
		}
	}
	if bestScore < 0 {
		return deskDisplayMode{}, false
	}
	return best, true
}

// deskSortModes 去重并按面积从大到小排序，方便 UI 直接展示。
func deskSortModes(in []deskDisplayMode) []deskDisplayMode {
	seen := map[[2]int]bool{}
	out := make([]deskDisplayMode, 0, len(in))
	for _, m := range in {
		if m.W <= 0 || m.H <= 0 {
			continue
		}
		k := [2]int{m.W, m.H}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, deskDisplayMode{W: m.W, H: m.H, Freq: m.Freq})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].W*out[i].H != out[j].W*out[j].H {
			return out[i].W*out[i].H > out[j].W*out[j].H
		}
		return out[i].W > out[j].W
	})
	return out
}

// deskApplyResolution 处理 set_resolution / reset_resolution 动作。
//
// w/h 为 0 且给了客户端窗口尺寸时，自动挑一个最贴近的模式（UI 上的"匹配我的窗口"）。
func deskApplyResolution(cap deskCapture, w, h, clientW, clientH int) (deskDisplayMode, error) {
	ctl, ok := cap.(deskResolutionCtl)
	if !ok {
		return deskDisplayMode{}, fmt.Errorf("当前平台不支持远程改分辨率")
	}
	if w <= 0 || h <= 0 {
		m, found := deskPickMode(ctl.Modes(), clientW, clientH)
		if !found {
			return deskDisplayMode{}, fmt.Errorf("没有可用的显示模式可匹配 %d×%d", clientW, clientH)
		}
		w, h = m.W, m.H
	}
	if err := ctl.SetMode(w, h); err != nil {
		return deskDisplayMode{}, err
	}
	return deskDisplayMode{W: w, H: h}, nil
}

// deskRestoreResolution 把分辨率恢复成会话开始时的样子。会话结束时无条件调用一次。
func deskRestoreResolution(cap deskCapture) error {
	ctl, ok := cap.(deskResolutionCtl)
	if !ok {
		return nil
	}
	return ctl.Restore()
}

// deskModesOf 供元信息上报使用：不支持的平台返回 nil，UI 据此隐藏这个入口。
func deskModesOf(cap deskCapture) []deskDisplayMode {
	ctl, ok := cap.(deskResolutionCtl)
	if !ok {
		return nil
	}
	return ctl.Modes()
}
