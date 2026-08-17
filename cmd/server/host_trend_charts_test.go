package main

import (
	"reflect"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// 主机「近期趋势」长期只画了采集到的指标里的一小半：swap、磁盘 IO 饱和度、运行时长、
// 内存/磁盘的绝对用量、接口与定时任务指标，采集端一直在报（shared.Metrics）、时序库一直
// 在存（vm.go 的 push）、历史接口一直在返回，页面上却一条都没有。排障的人于是要么去写
// PromQL，要么就当这些数据不存在——面板的价值在最后一步被丢掉了。
//
// 这条测试把「采到的就要画出来」变成机器可查的规则：以 shared.Metrics 的 JSON 字段为真源，
// 逐个要求它出现在经典控制台的主机趋势代码里。新增一个采集字段而忘了画，这里会红。
func TestHostTrendChartsCoverEveryCollectedMetric(t *testing.T) {
	js, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)

	// 这些字段不是标量，画不成"一条曲线"，但必须有各自的结构化图表。
	structured := map[string]string{
		"disks": "per-volume charts (chartDisk / chartDiskFree)",
		"gpus":  "per-GPU charts (chartGPU*)",
		"conns": "per-proto/state charts (chartConns / chartConnStates)",
	}
	// 根本不是指标：进程名列表喂的是「进程存活」检测，不存在趋势可言。
	notAMetric := map[string]bool{"process_names": true}

	rt := reflect.TypeOf(shared.Metrics{})
	var missing []string
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if notAMetric[tag] {
			continue
		}
		if why, ok := structured[tag]; ok {
			if !strings.Contains(src, tag) {
				t.Errorf("%s 没有对应的结构化图表（应有：%s）", tag, why)
			}
			continue
		}
		if !strings.Contains(src, tag) {
			missing = append(missing, tag)
		}
	}
	if len(missing) > 0 {
		t.Errorf("这些指标采集了、入库了、接口也返回了，但主机趋势里一条曲线都没有：%s\n"+
			"（在 cmd/server/web/js/hosts.js 的图表编排里加上；同单位的优先并进已有的组合图）",
			strings.Join(missing, ", "))
	}
}

// 组合曲线不是排版偏好，是这些图唯一能回答问题的形式：内存涨的时候 swap 有没有跟着涨、
// 负载升高时 CPU 是不是也升高、进程数与连接数是不是同步冲高。拆成单曲线图，这些关系就
// 得靠人脑对齐两张图的时间轴。
func TestHostTrendKeepsTheCorrelationCharts(t *testing.T) {
	js, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []struct{ id, why string }{
		{"chartCombo", "CPU/内存/交换/磁盘/IO 的饱和度总览"},
		{"chartMem", "内存与 swap 必须同图：只有内存曲线分不出缓存增长与真实内存压力"},
		{"chartMemBytes", "百分比回答余量，字节数回答还能不能再放一个实例"},
		{"chartLoad", "负载三条线 + 核数饱和线"},
		{"chartDiskFree", "剩余空间（GB）是容量规划唯一能直接用的量纲"},
		{"chartProc", "进程数与连接数同图才能分辨连接泄漏与 fork 失控"},
	} {
		if !strings.Contains(src, want.id) {
			t.Errorf("主机趋势少了 %s：%s", want.id, want.why)
		}
	}
}

// 阈值线是"数据支撑 → 闭环"的最后一公里：曲线说"现在 78%"，只有把 80/90 画在同一张图上，
// 它才同时回答了"离触发还有多远"。掉了这段，趋势图又变回一个需要人去别处对照的页面。
func TestHostTrendDrawsAlertThresholds(t *testing.T) {
	js, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "loadDetailThresholds") || !strings.Contains(src, "cfg.thresholds") {
		t.Fatal("趋势图不再读取告警阈值")
	}
	// 阈值取自 ThresholdConfig 的 JSON 字段名；改名而不同步这里，线会静默消失。
	for _, key := range []string{
		"TH.cpu_warn", "TH.cpu_crit", "TH.mem_warn", "TH.mem_crit",
		"TH.disk_warn", "TH.disk_crit", "TH.diskio_warn", "TH.iops_warn",
		"TH.load_warn", "TH.conn_warn", "TH.gpu_warn", "TH.gpu_temp_warn", "TH.gpu_mem_warn",
	} {
		if !strings.Contains(src, key) {
			t.Errorf("趋势图没有画 %s 对应的阈值线", key)
		}
	}
	// 拿不到阈值（例如 viewer 角色读不到 /config）时必须照常出图。
	if !strings.Contains(src, "DETAIL_THRESHOLDS || {}") {
		t.Fatal("阈值缺失时必须退化成不画线，而不是让图表报错")
	}
}

// 前端对缺字段的采样做 LOCF 补齐，靠的是一张手写的键列表。新画一条曲线却忘了把它的字段
// 加进去，那条线会在任何一个缺该字段的采样点上断开——看起来像"指标没了"，实际只是这一帧
// 的 JSON 少了个键。
func TestHostTrendLOCFCoversChartedFields(t *testing.T) {
	js, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	start := strings.Index(src, "function alignHistoryGaugeSamples")
	if start < 0 {
		t.Fatal("alignHistoryGaugeSamples 不见了")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("找不到 alignHistoryGaugeSamples 的结尾")
	}
	body := src[start : start+end]
	for _, field := range []string{
		"cpu_percent", "cpu_cores", "mem_percent", "mem_used", "mem_total",
		"swap_percent", "swap_used", "disk_percent", "disk_used", "disk_total",
		"disk_io_util_percent", "uptime",
		"api_avail_percent", "api_avg_resp_ms", "api_p95_resp_ms", "api_throughput_rps",
		"task_fail_count", "task_timeout_sec",
	} {
		if !strings.Contains(body, `"`+field+`"`) {
			t.Errorf("LOCF 键列表缺 %q：该曲线会在缺字段的采样点上断开", field)
		}
	}
}
