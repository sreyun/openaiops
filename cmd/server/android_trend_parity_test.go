package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// 手机端（android/）与经典控制台画的应当是同一批指标。
//
// 这条约束不是"好看"的问题：值班时手边往往只有手机，如果同一台主机在手机上看不到 swap、
// 磁盘剩余空间、运行时长、接口与任务指标，那么用电脑看和用手机看会得出**不同的结论**——
// 而做判断的人通常没机会知道自己看的是一个被裁剪过的视图。
//
// Android 侧没有 CI（也没有 JDK 可在本机编译），所以这条 Go 测试是唯一能自动跑的护栏：
// 它不验证 Kotlin 能不能编译，只验证"每个采集字段都在手机端的模型里被解析、并且真的被
// 画进了趋势目录"。字段名在两侧完全一致（Kotlin data class 直接用 json 名），所以文本
// 匹配足够可靠。
const (
	androidSampleModel  = "../../android/app/src/main/java/com/aiops/monitor/data/models/Models.kt"
	androidTrendCatalog = "../../android/app/src/main/java/com/aiops/monitor/ui/host/HostTrendCatalog.kt"
)

func TestAndroidHostTrendsCoverTheSameMetricsAsWeb(t *testing.T) {
	model, err := os.ReadFile(androidSampleModel)
	if err != nil {
		t.Skipf("没有 android 源码，跳过：%v", err)
	}
	catalog, err := os.ReadFile(androidTrendCatalog)
	if err != nil {
		t.Skipf("没有 android 趋势目录，跳过：%v", err)
	}
	modelSrc, catalogSrc := string(model), string(catalog)

	// 结构化字段各有专门的多曲线卡片，不是单条标量曲线。
	structured := map[string]bool{"disks": true, "gpus": true, "conns": true}
	// 有意不画的字段，每条都要写清理由——否则这个豁免表会变成"忘了画"的垃圾桶。
	exempt := map[string]string{
		"process_names": "不是指标：喂的是进程存活检测，没有趋势可言",
		"cpu_cores":     "Web 端用它在负载图上画一条饱和参考线；手机卡片窄，多一条常量线弊大于利，核数改在主机信息里显示",
	}

	rt := reflect.TypeOf(shared.Metrics{})
	var missingModel, missingChart []string
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, ok := exempt[tag]; ok {
			continue
		}
		if !strings.Contains(modelSrc, tag) {
			missingModel = append(missingModel, tag)
			continue
		}
		if structured[tag] {
			if !strings.Contains(catalogSrc, tag) {
				missingChart = append(missingChart, tag)
			}
			continue
		}
		if !strings.Contains(catalogSrc, tag) {
			missingChart = append(missingChart, tag)
		}
	}
	if len(missingModel) > 0 {
		t.Errorf("Android 的 Sample 模型没有解析这些字段，服务端返回了也会被丢掉：%s\n（补到 %s）",
			strings.Join(missingModel, ", "), androidSampleModel)
	}
	if len(missingChart) > 0 {
		t.Errorf("这些指标手机端解析了却没有画出来：%s\n（补到 %s 的 buildHostTrendSpecs；"+
			"同单位的优先并进已有的分组，与 Web 端保持一致）",
			strings.Join(missingChart, ", "), androidTrendCatalog)
	}
}

// 两端的口径也要一致：Web 端补上的那几条"关系型"曲线（连接总数、进程+连接同图、
// 磁盘剩余空间、内存绝对用量），在手机端同样是判断的依据，不能只有一半。
func TestAndroidTrendKeepsTheSameCorrelationSeries(t *testing.T) {
	catalog, err := os.ReadFile(androidTrendCatalog)
	if err != nil {
		t.Skipf("没有 android 趋势目录，跳过：%v", err)
	}
	src := string(catalog)
	for _, want := range []struct{ id, why string }{
		{`"conn_all"`, "告警判的是 TCP+UDP 合计，总数必须自成一条线"},
		{`"net_conns"`, "进程数与连接数同图才分得出连接泄漏与 fork 失控"},
		{`"mem_used_gb"`, "百分比回答余量，字节数回答还能不能再放一个实例"},
		{`"diskfree:`, "剩余空间（GB）是容量规划唯一能直接用的量纲"},
		{`"uptime_h"`, "运行时长掉回 0 = 主机重启过，能解释掉一整段指标断裂"},
	} {
		if !strings.Contains(src, want.id) {
			t.Errorf("手机端缺少 %s：%s", want.id, want.why)
		}
	}
	// TCP 状态两端要一样：少掉的这三个恰恰是排查"连不上对端"和"连接没被回收"的第一现场。
	for _, st := range []string{"SYN_SENT", "FIN_WAIT1", "FIN_WAIT2"} {
		if !strings.Contains(src, st) {
			t.Errorf("手机端的 TCP 状态曲线缺 %s（Web 端已有）", st)
		}
	}
}
