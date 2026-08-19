package main

import "testing"

// Dell 的 GPU 采集缺失与厂商路径无关：iDRAC8(R730 一代) 的 Processors 集合
// **按设计只有 CPU**，独显根本不在里面，只能从 PCIe 侧找。
// iDRAC9 则把独显作为 ProcessorType=GPU 的 Processors 成员给出。
// 这两条路径都要有回归覆盖。

// idrac8Routes 复刻 R730/iDRAC8：Processors 只有 CPU；PCIe 设备挂在
// **Systems 下、单数 PCIeDevice**；且 iDRAC8 明确不上报 Model/PartNumber。
func idrac8Routes() map[string]string {
	return map[string]string{
		"/redfish/v1/Systems":                   `{"Members":[{"@odata.id":"/redfish/v1/Systems/System.Embedded.1"}]}`,
		"/redfish/v1/Chassis":                   `{"Members":[{"@odata.id":"/redfish/v1/Chassis/System.Embedded.1"}]}`,
		"/redfish/v1/Managers":                  `{"Members":[{"@odata.id":"/redfish/v1/Managers/iDRAC.Embedded.1"}]}`,
		"/redfish/v1/Managers/iDRAC.Embedded.1": `{"Model":"iDRAC8","FirmwareVersion":"2.70.70.70"}`,
		"/redfish/v1/Systems/System.Embedded.1": `{
			"Status":{"Health":"OK","State":"Enabled"},
			"Manufacturer":"Dell Inc.","Model":"PowerEdge R730","SKU":"3X9L1N2","BiosVersion":"2.14.0","PowerState":"On",
			"MemorySummary":{"TotalSystemMemoryGiB":128},
			"Processors":{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/Processors"},
			"PCIeDevices":[
				{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/PCIeDevice/4-0"},
				{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/PCIeDevice/74-0"}],
			"Links":{"Chassis":[{"@odata.id":"/redfish/v1/Chassis/System.Embedded.1"}]}}`,
		// iDRAC8 的 Processors 集合按设计只有 CPU —— 这正是"Dell 采不到 GPU"的根因
		"/redfish/v1/Systems/System.Embedded.1/Processors": `{"Members":[
			{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/Processors/CPU.Socket.1"}]}`,
		"/redfish/v1/Systems/System.Embedded.1/Processors/CPU.Socket.1": `{
			"Name":"CPU 1","Model":"Intel(R) Xeon(R) CPU E5-2680 v4","ProcessorType":"CPU",
			"TotalCores":14,"TotalThreads":28,"MaxSpeedMHz":3300,"Status":{"Health":"OK","State":"Enabled"}}`,
		// 板载 Matrox：DeviceClass 同样是 DisplayController，只有 ClassCode 能区分
		"/redfish/v1/Systems/System.Embedded.1/PCIeDevice/4-0": `{
			"Name":"Integrated Matrox G200eW3 Graphics Controller","Manufacturer":"Matrox",
			"Status":{"Health":"OK","State":"Enabled"},
			"Links":{"PCIeFunctions":[{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/PCIeFunction/4-0-0"}]}}`,
		"/redfish/v1/Systems/System.Embedded.1/PCIeFunction/4-0-0": `{
			"ClassCode":"0x030000","DeviceClass":"DisplayController",
			"Name":"Integrated Matrox G200eW3 Graphics Controller","VendorId":"0x102b","DeviceId":"0x0536"}`,
		// 真独显：iDRAC8 不给 Model，只能靠 PCIeFunction 的 Name 兜底
		"/redfish/v1/Systems/System.Embedded.1/PCIeDevice/74-0": `{
			"Name":"NVIDIA Tesla P100","Manufacturer":"NVIDIA Corporation",
			"Status":{"Health":"OK","State":"Enabled"},
			"Links":{"PCIeFunctions":[{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/PCIeFunction/74-0-0"}]}}`,
		"/redfish/v1/Systems/System.Embedded.1/PCIeFunction/74-0-0": `{
			"ClassCode":"0x030200","DeviceClass":"DisplayController",
			"Name":"GP100GL [Tesla P100 PCIe 16GB]","VendorId":"0x10de","DeviceId":"0x15f8"}`,
		"/redfish/v1/Chassis/System.Embedded.1":         `{"Thermal":{"@odata.id":"/redfish/v1/Chassis/System.Embedded.1/Thermal"}}`,
		"/redfish/v1/Chassis/System.Embedded.1/Thermal": `{"Temperatures":[],"Fans":[]}`,
	}
}

func TestIDRAC8GPUFoundViaPCIe(t *testing.T) {
	srv := serveRoutes(t, idrac8Routes())
	defer srv.Close()

	rc := newRedfishCollector(nil, "h3", "fp")
	snap := rc.collectOne(RedfishTarget{Name: "idrac8", URL: srv.URL, Username: "u", Password: "p"})
	if snap.Error != "" {
		t.Fatalf("collectOne error = %q", snap.Error)
	}
	// CPU 仍走标准路径
	if len(snap.CPUs) != 1 || snap.CPUs[0].Cores != 14 {
		t.Errorf("CPUs = %+v", snap.CPUs)
	}
	// 关键：Processors 里没有 GPU，必须从 PCIe 兜底找到
	if len(snap.GPUs) != 1 {
		t.Fatalf("GPUs = %d, want 1（iDRAC8 的 GPU 只能从 PCIeDevices 拿到）: %+v", len(snap.GPUs), snap.GPUs)
	}
	g := snap.GPUs[0]
	// 板载 Matrox 绝不能被当成 GPU 报出来
	if g.Manufacturer == "Matrox" {
		t.Fatal("板载 Matrox 被误判为 GPU —— ClassCode 0x030000(VGA) 必须排除")
	}
	if g.Manufacturer != "NVIDIA Corporation" {
		t.Errorf("GPU.Manufacturer = %q", g.Manufacturer)
	}
	// iDRAC8 不给 Model，应回落到 PCIeFunction 的 Name
	if g.Model != "GP100GL [Tesla P100 PCIe 16GB]" {
		t.Errorf("GPU.Model = %q, want 回落到 PCIeFunction.Name", g.Model)
	}
}

// iDRAC9 已经在 Processors 里给出 GPU，就不该再去扫 PCIe（白白几十次 GET）。
func TestIDRAC9GPUFromProcessorsSkipsPCIe(t *testing.T) {
	routes := dellRoutes()
	routes["/redfish/v1/Systems/System.Embedded.1/Processors"] = `{"Members":[
		{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/Processors/CPU.Socket.1"},
		{"@odata.id":"/redfish/v1/Systems/System.Embedded.1/Processors/Video.Slot.38-1"}]}`
	routes["/redfish/v1/Systems/System.Embedded.1/Processors/Video.Slot.38-1"] = `{
		"Name":"Video.Slot.38-1","Model":"NVIDIA H100 PCIe","Manufacturer":"NVIDIA Corporation",
		"ProcessorType":"GPU","MaxSpeedMHz":1755,"Status":{"Health":"OK","State":"Enabled"}}`

	srv := serveRoutes(t, routes)
	defer srv.Close()

	rc := newRedfishCollector(nil, "h4", "fp")
	snap := rc.collectOne(RedfishTarget{Name: "idrac9", URL: srv.URL, Username: "u", Password: "p"})
	if len(snap.GPUs) != 1 || snap.GPUs[0].Model != "NVIDIA H100 PCIe" {
		t.Fatalf("GPUs = %+v, want H100 来自 Processors", snap.GPUs)
	}
	if len(snap.CPUs) != 1 {
		t.Errorf("CPUs = %+v, want GPU 不混进 CPU 列表", snap.CPUs)
	}
	// Processors 已给出 GPU → 不应触发 PCIe 兜底
	rc.mu.Lock()
	_, scanned := rc.lastPCIe["idrac9"]
	rc.mu.Unlock()
	if scanned {
		t.Error("Processors 已有 GPU 仍扫了 PCIe —— 白白增加几十次 BMC 请求")
	}
}

func TestIsGPUClassCode(t *testing.T) {
	// 0x0302xx = 3D 控制器（独显/计算卡）；0x0300xx = VGA（板载 Matrox）
	for _, c := range []string{"0x030200", "030200", "0X030200", "0x0302ff"} {
		if !isGPUClassCode(c) {
			t.Errorf("isGPUClassCode(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"0x030000", "0x020000", "0x010802", "", "0x0380"} {
		if isGPUClassCode(c) {
			t.Errorf("isGPUClassCode(%q) = true, want false", c)
		}
	}
}
