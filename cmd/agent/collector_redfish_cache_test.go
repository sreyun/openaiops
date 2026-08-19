package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// 降频采集的字段（固件 / SEL / PCIe GPU）必须**每轮都随快照带上**。
// 快照在服务端是整体 upsert 的：某一轮不带 = 把上次采到的抹掉。
// 固件此前正是这个 bug —— 每小时才写进一次快照，其余 59 轮全空，
// 于是"固件版本"在界面上几乎永远是空的。

func firmwareRoutes(hits *int32) map[string]string {
	return map[string]string{
		"/redfish/v1":               `{"UpdateService":{"@odata.id":"/redfish/v1/UpdateService"}}`,
		"/redfish/v1/UpdateService": `{"FirmwareInventory":{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory"}}`,
		"/redfish/v1/UpdateService/FirmwareInventory": `{"Members":[
			{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/BIOS"},
			{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/iBMC"},
			{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/Empty"}]}`,
		"/redfish/v1/UpdateService/FirmwareInventory/BIOS": `{"Name":"BIOS","Version":"1.79"}`,
		"/redfish/v1/UpdateService/FirmwareInventory/iBMC": `{"Name":"iBMC","Version":"6.22.00.00"}`,
		// 没有版本号的条目对运维没意义，应被丢弃
		"/redfish/v1/UpdateService/FirmwareInventory/Empty": `{"Name":"Placeholder","Version":""}`,
	}
}

// serveCounting 统计固件清单被拉取的次数，用来验证降频真的生效。
func serveCounting(t *testing.T, base, fwRoutes map[string]string, invHits *int32) *httptest.Server {
	t.Helper()
	all := map[string]string{}
	for k, v := range base {
		all[k] = v
	}
	for k, v := range fwRoutes {
		all[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redfish/v1/UpdateService/FirmwareInventory" {
			atomic.AddInt32(invHits, 1)
		}
		if body, ok := all[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFirmwarePersistsAcrossPolls(t *testing.T) {
	var hits int32
	srv := serveCounting(t, huaweiRoutes(), firmwareRoutes(&hits), &hits)

	rc := newRedfishCollector(nil, "h1", "fp")
	tgt := RedfishTarget{Name: "ibmc", URL: srv.URL, Username: "u", Password: "p"}

	first := rc.collectOne(tgt)
	if len(first.Firmware) != 2 {
		t.Fatalf("首轮固件 = %+v, want 2 条（无版本号的应丢弃）", first.Firmware)
	}
	// 排序稳定，便于比对
	if first.Firmware[0].Name != "BIOS" || first.Firmware[0].Version != "1.79" {
		t.Errorf("固件[0] = %+v", first.Firmware[0])
	}

	// 第二轮未到刷新周期 —— 关键断言：固件不能变空
	second := rc.collectOne(tgt)
	if len(second.Firmware) != 2 {
		t.Fatalf("第二轮固件 = %d 条, want 2（降频轮次必须沿用缓存，否则快照 upsert 会把固件抹掉）",
			len(second.Firmware))
	}
	// 且不应重复拉取
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("固件清单被拉取 %d 次, want 1（应按小时降频）", n)
	}
}

func TestFirmwareFollowsUpdateServiceLink(t *testing.T) {
	// 固件挂在非标准路径上：只有跟随 ServiceRoot→UpdateService→FirmwareInventory 才找得到
	base := huaweiRoutes()
	custom := map[string]string{
		"/redfish/v1":                   `{"UpdateService":{"@odata.id":"/redfish/v1/UpdateSvc"}}`,
		"/redfish/v1/UpdateSvc":         `{"FirmwareInventory":{"@odata.id":"/redfish/v1/UpdateSvc/FwInv"}}`,
		"/redfish/v1/UpdateSvc/FwInv":   `{"Members":[{"@odata.id":"/redfish/v1/UpdateSvc/FwInv/1"}]}`,
		"/redfish/v1/UpdateSvc/FwInv/1": `{"Name":"iBMC","Version":"6.22.00.00"}`,
	}
	var hits int32
	srv := serveCounting(t, base, custom, &hits)

	rc := newRedfishCollector(nil, "h1", "fp")
	snap := rc.collectOne(RedfishTarget{Name: "ibmc", URL: srv.URL, Username: "u", Password: "p"})
	if len(snap.Firmware) != 1 || snap.Firmware[0].Version != "6.22.00.00" {
		t.Fatalf("固件 = %+v, want 经 UpdateService 链接找到（不能硬拼标准路径）", snap.Firmware)
	}
}

// SEL 同理：降频轮次必须沿用缓存。
func TestEventsPersistAcrossPolls(t *testing.T) {
	srv := serveRoutes(t, huaweiRoutes())
	defer srv.Close()

	rc := newRedfishCollector(nil, "h1", "fp")
	tgt := RedfishTarget{Name: "ibmc", URL: srv.URL, Username: "u", Password: "p"}

	first := rc.collectOne(tgt)
	if len(first.Events) == 0 {
		t.Fatal("首轮应采到事件")
	}
	second := rc.collectOne(tgt)
	if len(second.Events) != len(first.Events) {
		t.Errorf("第二轮事件 = %d, want %d（降频轮次必须沿用缓存）", len(second.Events), len(first.Events))
	}
}
