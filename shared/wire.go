// Package shared holds the wire types exchanged between the Go agent core
// and the Go backend. Keeping them in one place is exactly the "share code
// with the backend" benefit of the hybrid architecture: the collector, the
// reporter and the server all speak the same structs, so the contract can
// never drift.
package shared

// Metrics is one point-in-time snapshot of base system metrics.
// On Linux/Windows/macOS the agent core fills this natively (procfs+syscall /
// Win32 API / sysctl). On any other platform it can be supplied by a core
// plugin (e.g. psutil) behind the same Collector interface.
type Metrics struct {
	CPUPercent   float64    `json:"cpu_percent"`
	CPUCores     int        `json:"cpu_cores"`
	MemTotal     uint64     `json:"mem_total"`
	MemUsed      uint64     `json:"mem_used"`
	MemPercent   float64    `json:"mem_percent"`
	SwapTotal    uint64     `json:"swap_total"`
	SwapUsed     uint64     `json:"swap_used"`
	SwapPercent  float64    `json:"swap_percent"`
	DiskTotal    uint64     `json:"disk_total"`
	DiskUsed     uint64     `json:"disk_used"`
	DiskPercent  float64    `json:"disk_percent"`
	Disks        []DiskInfo `json:"disks,omitempty"` // per-volume usage for every local disk
	NetSentRate  float64    `json:"net_sent_rate"`
	NetRecvRate  float64    `json:"net_recv_rate"`
	NetConns     int        `json:"net_conns"`       // established TCP connections (兼容旧字段)
	Conns        []ConnStat `json:"conns,omitempty"` // per-proto/per-state socket 计数（TCP 各状态 + UDP 总数）
	Load1        float64    `json:"load1"`
	Load5        float64    `json:"load5"`
	Load15       float64    `json:"load15"`
	ProcCount    int        `json:"proc_count"`
	Uptime       uint64     `json:"uptime"`
	GPUs         []GPUInfo  `json:"gpus,omitempty"`          // per-GPU utilization / VRAM (best-effort, cross-platform)
	ProcessNames []string   `json:"process_names,omitempty"` // top process names for process-monitor checks
	// Disk IO: read/write rates (bytes/sec) and IO utilization percentage
	DiskReadRate      float64 `json:"disk_read_rate"`
	DiskWriteRate     float64 `json:"disk_write_rate"`
	DiskIOUtilPercent float64 `json:"disk_io_util_percent"`
	// Disk IOPS: read/write operations per second
	DiskReadIOPS  float64 `json:"disk_read_iops"`
	DiskWriteIOPS float64 `json:"disk_write_iops"`

	// ---- API 业务监控指标（由插件或外部系统上报，可选）----
	APIAvailPercent  float64 `json:"api_avail_percent,omitempty"`  // 接口可用率 %
	APIAvgRespMs     float64 `json:"api_avg_resp_ms,omitempty"`    // 平均响应时间 ms
	APIP95RespMs     float64 `json:"api_p95_resp_ms,omitempty"`    // P95 响应时间 ms
	APIThroughputRPS float64 `json:"api_throughput_rps,omitempty"` // 吞吐量 req/s

	// ---- 编排定时任务指标（由插件或外部系统上报，可选）----
	TaskFailCount  int     `json:"task_fail_count,omitempty"`  // 执行失败次数
	TaskTimeoutSec float64 `json:"task_timeout_sec,omitempty"` // 超时时长 s
}

// GPUInfo is per-GPU usage. Collection is best-effort and vendor-dependent:
// NVIDIA via nvidia-smi (Linux/Windows), AMD via sysfs (Linux), Apple/other via
// ioreg (macOS). Fields that a platform cannot supply are left zero.
type GPUInfo struct {
	Name        string  `json:"name"`
	UtilPercent float64 `json:"util_percent"`
	MemUsed     uint64  `json:"mem_used,omitempty"`
	MemFree     uint64  `json:"mem_free,omitempty"` // VRAM 空闲字节（total-used 或直接采集）
	MemTotal    uint64  `json:"mem_total,omitempty"`
	MemPercent  float64 `json:"mem_percent,omitempty"`
	Temp        float64 `json:"temp,omitempty"` // °C, 0 if unknown
}

// ConnStat is a per-protocol, per-state socket count, powering the connection /
// session-state trend charts. For TCP, State is a canonical state name
// (ESTABLISHED/TIME_WAIT/LISTEN/CLOSE_WAIT/SYN_SENT/...); for UDP (which is
// stateless) a single entry with State="" carries the total socket count.
type ConnStat struct {
	Proto string `json:"proto"`           // "tcp" | "udp"
	State string `json:"state,omitempty"` // TCP 状态名；UDP 为空
	Count int    `json:"count"`
}

// DiskInfo is per-volume disk usage. The agent enumerates every local disk:
// all fixed drives on Windows (C:, D:, …), real filesystem mounts on
// Linux/macOS. Path is the drive letter or mount point.
type DiskInfo struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
}

// Sample is a Metrics snapshot stamped with the server receive time.
type Sample struct {
	Timestamp int64 `json:"timestamp"`
	Metrics
}

// LogLine is one collected log line from an agent's log sources.
type LogLine struct {
	Ts      int64  `json:"ts"`
	Source  string `json:"source"` // file path / "journald" / "docker:<name>"
	Level   string `json:"level"`  // error|warn|info|debug
	Message string `json:"message"`
}

// LogBatch is a batch of collected log lines POSTed by an agent. The agent
// authenticates via the X-Agent-Fingerprint header (like the terminal + forward
// channels), so no credential travels in the body.
type LogBatch struct {
	HostID string    `json:"host_id"`
	Lines  []LogLine `json:"lines"`
}

// Event is a discrete signal emitted by a plugin — this is the channel the
// Python plugin / AI / automation layer uses to raise findings (anomalies,
// service-down, predictions, remediation results...).
type Event struct {
	Timestamp int64  `json:"timestamp"`
	Level     string `json:"level"`  // info | warning | critical
	Source    string `json:"source"` // plugin name
	Message   string `json:"message"`
}

// Report is the payload the agent core POSTs each cycle. Base metrics come
// from the Go core; Custom gauges and Events come from the plugin layer.
// Category is an operator-defined group label (e.g. prod / db / office-endpoint)
// used by the dashboard to group and filter hosts.
type Report struct {
	HostID      string             `json:"host_id"`
	Hostname    string             `json:"hostname"`
	OS          string             `json:"os"`
	Platform    string             `json:"platform"` // OS / distribution version
	Arch        string             `json:"arch"`
	IP          string             `json:"ip,omitempty"`
	Kernel      string             `json:"kernel,omitempty"`
	Category string `json:"category,omitempty"`
	// FolderID is the asset-tree node id from install (any depth). Prefer this over
	// Category for placement; Category remains the leaf label / legacy L1 hint.
	FolderID string `json:"folder_id,omitempty"`
	// AgentVersion is the running agent binary version (ldflags -X main.appVersion).
	AgentVersion string `json:"agent_version,omitempty"`
	// ServerURL is the agent's configured primary report base (relay or cloud).
	// Server uses this for fleet update /dl URLs so intranet agents download via relay.
	ServerURL   string `json:"server_url,omitempty"`
	Token       string `json:"token,omitempty"`       // install token (registration only)
	Fingerprint string `json:"fingerprint,omitempty"` // machine fingerprint (machine-id+MAC), authenticates reports
	Metrics      Metrics `json:"metrics"`
	Custom      map[string]float64 `json:"custom,omitempty"`
	Events      []Event            `json:"events,omitempty"`
	// Desktop is optional probe of local RDP/VNC listeners for remote desktop mode.
	Desktop *DesktopInfo `json:"desktop,omitempty"`
}

// DesktopInfo describes whether the agent host appears to offer a GUI remote
// protocol (RDP / VNC) and which port the dashboard should tunnel to.
type DesktopInfo struct {
	OS            string `json:"os,omitempty"`
	RDP           bool   `json:"rdp"`
	VNC           bool   `json:"vnc"`
	Ports         []int  `json:"ports,omitempty"`
	Preferred     string `json:"preferred,omitempty"`      // "rdp" | "vnc"
	PreferredPort int    `json:"preferred_port,omitempty"`
}

// ============================================================================
// Redfish 硬件状态采集结构体
// ============================================================================

// HardwareSnapshot is one point-in-time snapshot of a Redfish-managed server.
type HardwareSnapshot struct {
	TargetName string             `json:"target_name"`
	TargetURL  string             `json:"target_url"`
	Timestamp  int64              `json:"timestamp"`
	Health     string             `json:"health"` // OK / Warning / Critical
	State      string             `json:"state"`  // Enabled / Disabled / ...
	System     RedfishSystem      `json:"system"` // 整机身份（厂商/型号/序列号/BIOS…）
	CPUs       []RedfishCPU       `json:"cpus"`
	GPUs       []RedfishGPU       `json:"gpus,omitempty"` // Processors 里 ProcessorType=GPU 的成员
	Memory     RedfishMemory      `json:"memory"`
	Storage    []RedfishStorage   `json:"storage"`
	RAID       []RedfishRAID      `json:"raid,omitempty"`       // Storage 成员里的 StorageControllers（RAID 卡）
	Enclosures []StorageEnclosure `json:"enclosures,omitempty"` // 磁盘框（OceanStor 等外置存储）
	Temps      []SensorReading    `json:"temps"`
	Fans       []FanReading       `json:"fans"`
	Power      RedfishPower       `json:"power"`
	Firmware   []FirmwareInfo     `json:"firmware,omitempty"` // 降频采集
	Events     []HardwareEvent    `json:"events,omitempty"`   // BMC SEL / 事件日志（最近若干条）
	Error      string             `json:"error,omitempty"`
}

// RedfishSystem is the chassis/system identity — who this machine actually is.
// Without it the UI can only show a BMC URL, so an operator staring at a fault
// can't tell an R730 from a TaiShan 200 without opening iDRAC/iBMC by hand.
type RedfishSystem struct {
	Manufacturer string `json:"manufacturer,omitempty"` // Dell Inc. / Huawei
	Model        string `json:"model,omitempty"`        // PowerEdge R740 / RH2288 V3 / TaiShan 200 (Model 2280)
	SKU          string `json:"sku,omitempty"`          // Dell 的 Service Tag 就在这里
	SerialNumber string `json:"serial_number,omitempty"`
	AssetTag     string `json:"asset_tag,omitempty"`
	HostName     string `json:"host_name,omitempty"` // BMC 视角的 OS 主机名
	BIOSVersion  string `json:"bios_version,omitempty"`
	PowerState   string `json:"power_state,omitempty"` // On / Off
	IndicatorLED string `json:"indicator_led,omitempty"`
	BMCModel     string `json:"bmc_model,omitempty"`    // iDRAC9 / iBMC
	BMCFirmware  string `json:"bmc_firmware,omitempty"` // Manager.FirmwareVersion
	// 存储阵列（OceanStor 等）专有：服务器 BMC 不上报这些，故 omitempty。
	SoftwareVersion string  `json:"software_version,omitempty"`  // 阵列软件版本，如 V300R003C20
	PatchVersion    string  `json:"patch_version,omitempty"`     // 补丁版本，如 SPC200 SPH216
	Location        string  `json:"location,omitempty"`          // 设备位置（DeviceManager 里手填），如 hcidc
	TotalCapacityGB float64 `json:"total_capacity_gb,omitempty"` // 阵列总容量
	UsedCapacityGB  float64 `json:"used_capacity_gb,omitempty"`  // 已用容量
}

// HardwareEvent is one BMC log entry (Dell iDRAC SEL/LC log, Huawei iBMC event
// log). This is the ONLY source that says *which* component caused a fault and
// when — a Health=Critical rollup on its own tells an operator nothing.
type HardwareEvent struct {
	ID        string `json:"id,omitempty"`
	Created   string `json:"created,omitempty"`  // RFC3339 from the BMC
	Severity  string `json:"severity,omitempty"` // OK / Warning / Critical
	Message   string `json:"message"`
	MessageID string `json:"message_id,omitempty"` // e.g. "AMP0300" / "Alert.1.0.PowerSupply"
	// Component is the offending part, resolved from Links.OriginOfCondition or
	// the sensor/entry metadata — "PSU 2", "DIMM A3", "Disk 0:1:3", …
	Component  string `json:"component,omitempty"`
	SensorType string `json:"sensor_type,omitempty"`
	Resolved   bool   `json:"resolved,omitempty"`
}

type RedfishCPU struct {
	Name       string  `json:"name"`
	Model      string  `json:"model"`
	Cores      int     `json:"cores"`
	Threads    int     `json:"threads"`
	Health     string  `json:"health"`
	TempC      float64 `json:"temp_c,omitempty"`
	MaxFreqMHz int     `json:"max_freq_mhz,omitempty"`
}

// RedfishGPU is a GPU reported by the BMC (a Processors member whose
// ProcessorType is "GPU"). Distinct from Metrics.GPUs, which is the OS-side
// nvidia-smi view — this one works even when the host OS is down.
type RedfishGPU struct {
	Name         string `json:"name"`
	Model        string `json:"model,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Health       string `json:"health"`
	State        string `json:"state,omitempty"`
	MaxFreqMHz   int    `json:"max_freq_mhz,omitempty"`
}

// RedfishRAID is a storage/RAID controller (Storage member's StorageControllers).
type RedfishRAID struct {
	Name            string          `json:"name"`
	Model           string          `json:"model,omitempty"`
	Manufacturer    string          `json:"manufacturer,omitempty"`
	FirmwareVersion string          `json:"firmware_version,omitempty"`
	SpeedGbps       float64         `json:"speed_gbps,omitempty"`
	Health          string          `json:"health"`
	State           string          `json:"state,omitempty"`
	DriveCount      int             `json:"drive_count,omitempty"`
	SerialNumber    string          `json:"serial_number,omitempty"`
	CacheMB         float64         `json:"cache_mb,omitempty"`     // CacheSummary.TotalCacheSizeMiB
	CacheHealth     string          `json:"cache_health,omitempty"` // 掉电保护/BBU 状态
	Volumes         []RedfishVolume `json:"volumes,omitempty"`
}

// RedfishVolume is a logical RAID volume (Storage member's Volumes).
type RedfishVolume struct {
	Name       string  `json:"name"`
	RAIDType   string  `json:"raid_type,omitempty"` // RAID0 / RAID1 / RAID5…
	CapacityGB float64 `json:"capacity_gb,omitempty"`
	Health     string  `json:"health,omitempty"`
	State      string  `json:"state,omitempty"`
}

// StorageEnclosure is a disk enclosure (磁盘框). Populated by the OceanStor
// DeviceManager collector — OceanStor arrays expose no Redfish endpoint at all,
// so this never comes from a BMC.
type StorageEnclosure struct {
	Name         string  `json:"name"`
	Model        string  `json:"model,omitempty"`
	SerialNumber string  `json:"serial_number,omitempty"`
	Location     string  `json:"location,omitempty"`
	Type         string  `json:"type,omitempty"`
	Health       string  `json:"health"`
	State        string  `json:"state,omitempty"`
	TemperatureC float64 `json:"temperature_c,omitempty"`
}

type RedfishMemory struct {
	TotalGB float64      `json:"total_gb"`
	UsedGB  float64      `json:"used_gb,omitempty"`
	DIMMs   []MemoryDIMM `json:"dimms,omitempty"`
}

type MemoryDIMM struct {
	Name         string  `json:"name"`
	CapacityGB   float64 `json:"capacity_gb"`
	Type         string  `json:"type"` // DDR4 / DDR5
	SpeedMHz     int     `json:"speed_mhz"`
	Health       string  `json:"health"`
	Slot         string  `json:"slot,omitempty"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	PartNumber   string  `json:"part_number,omitempty"`
	SerialNumber string  `json:"serial_number,omitempty"`
	RankCount    int     `json:"rank_count,omitempty"`
	State        string  `json:"state,omitempty"` // Enabled / Absent
}

type RedfishStorage struct {
	Name       string  `json:"name"`
	Model      string  `json:"model,omitempty"`
	CapacityGB float64 `json:"capacity_gb"`
	Health     string  `json:"health"`
	MediaType  string  `json:"media_type,omitempty"` // HDD / SSD / NVMe
	Protocol   string  `json:"protocol,omitempty"`   // SATA / SAS / NVMe
	Status     string  `json:"status,omitempty"`     // OK / Warning / Critical
	SMARTWarn  bool    `json:"smart_warn,omitempty"`
	// 定位与寿命：换盘要知道插在哪个槽位，SSD 还要知道还剩多少寿命。
	SerialNumber string  `json:"serial_number,omitempty"`
	Revision     string  `json:"revision,omitempty"` // 盘固件版本
	Location     string  `json:"location,omitempty"` // 槽位，如 "Bay 3" / "Disk 0:1:3"
	Manufacturer string  `json:"manufacturer,omitempty"`
	RotationRPM  int     `json:"rotation_rpm,omitempty"`
	LifeLeftPct  float64 `json:"life_left_pct,omitempty"` // SSD 剩余寿命；-1 = 未知
	SpeedGbps    float64 `json:"speed_gbps,omitempty"`
	HotspareType string  `json:"hotspare_type,omitempty"`
	State        string  `json:"state,omitempty"`
}

type SensorReading struct {
	Name          string  `json:"name"`
	Reading       float64 `json:"reading"`
	Unit          string  `json:"unit"`   // Celsius, etc.
	Status        string  `json:"status"` // OK / Warning / Critical
	UpperCaution  float64 `json:"upper_caution,omitempty"`
	UpperCritical float64 `json:"upper_critical,omitempty"`
}

type FanReading struct {
	Name   string `json:"name"`
	RPM    int    `json:"rpm"`
	Health string `json:"health"`
	Status string `json:"status,omitempty"`
}

type RedfishPower struct {
	Redundancy string       `json:"redundancy"` // Full / N+1 / NotRedundant
	PSUs       []PSUReading `json:"psus,omitempty"`
	TotalWatts float64      `json:"total_watts,omitempty"`
}

type PSUReading struct {
	Name             string  `json:"name"`
	InputWatts       float64 `json:"input_watts"`
	OutputWatts      float64 `json:"output_watts,omitempty"`
	Health           string  `json:"health"`
	State            string  `json:"state"`
	Model            string  `json:"model,omitempty"`
	Manufacturer     string  `json:"manufacturer,omitempty"`
	SerialNumber     string  `json:"serial_number,omitempty"`
	FirmwareVersion  string  `json:"firmware_version,omitempty"`
	CapacityWatts    float64 `json:"capacity_watts,omitempty"` // 额定功率
	LineInputVoltage float64 `json:"line_input_voltage,omitempty"`
	PowerSupplyType  string  `json:"psu_type,omitempty"` // AC / DC
}

type FirmwareInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HardwareReport is the payload agents POST for Redfish hardware snapshots.
type HardwareReport struct {
	HostID      string             `json:"host_id"`
	Fingerprint string             `json:"fingerprint,omitempty"`
	Snapshots   []HardwareSnapshot `json:"snapshots"`
}

// ============================================================================
// Hyper-V 虚拟机采集结构体（宿主机上的 Guest VM 清单）
//
// 与硬件快照同属"一台主机一份、变化慢、要追踪变更"的清单类数据，因此走独立
// 上报通道（POST /api/v1/agent/hyperv）而非高频 metrics 热路径。数据由物理
// 宿主机上的 Windows agent 通过 PowerShell(Get-VM) 采集。
// ============================================================================

// HyperVGuest is one Hyper-V guest VM as seen from the physical host.
// Health 由 agent 侧从 State/Status/ReplicationHealth 归一而来（OK/Warning/
// Critical），让服务端告警评估无需重复解析厂商字符串。
type HyperVGuest struct {
	Name              string   `json:"name"`
	ID                string   `json:"id"`                      // VM GUID：稳定身份，用于变更追踪（改名也认得出是同一台）
	State             string   `json:"state"`                   // Running / Off / Paused / Saved / Starting / ...
	Status            string   `json:"status,omitempty"`        // "Operating normally" / 降级/故障描述
	Health            string   `json:"health,omitempty"`        // OK / Warning / Critical（归一后）
	CPUUsage          float64  `json:"cpu_usage"`               // 宿主视角 CPU 占用 %（该 VM 占整机 CPU 的比例）
	CPUGuestPct       float64  `json:"cpu_guest_pct,omitempty"` // 客户机视角 CPU 利用率 %（占该 VM 自身 vCPU 的比例，0~100）
	ProcessorCount    int      `json:"processor_count,omitempty"`
	MemAssignedMB     float64  `json:"mem_assigned_mb,omitempty"`
	MemDemandMB       float64  `json:"mem_demand_mb,omitempty"`
	MemStartupMB      float64  `json:"mem_startup_mb,omitempty"`
	MemMinMB          float64  `json:"mem_min_mb,omitempty"`
	MemMaxMB          float64  `json:"mem_max_mb,omitempty"`
	DynamicMemEnabled bool     `json:"dynamic_mem_enabled,omitempty"` // 内存压力(需求/分配)只对动态内存 VM 有意义
	UptimeSec         int64    `json:"uptime_sec,omitempty"`
	Generation        int      `json:"generation,omitempty"`
	Version           string   `json:"version,omitempty"`
	IntegrationState  string   `json:"integration_state,omitempty"` // 集成服务状态（展示用，可能本地化）
	IPAddresses       []string `json:"ip_addresses,omitempty"`      // 所有网卡 IP 汇总（由集成服务上报，Guest 运行时才有）
	Switches          []string `json:"switches,omitempty"`          // 连接的虚拟交换机名
	VHDCount          int      `json:"vhd_count,omitempty"`
	CheckpointCount   int      `json:"checkpoint_count,omitempty"`
	ReplState         string   `json:"repl_state,omitempty"`  // Disabled / Enabled / ...
	ReplHealth        string   `json:"repl_health,omitempty"` // NotApplicable / Normal / Warning / Critical
	// 明细（用于前端 VM 详情视图；老 Agent 不上报时为空，前端优雅降级）
	Disks       []HyperVDisk       `json:"disks,omitempty"`
	Nics        []HyperVNic        `json:"nics,omitempty"`
	Checkpoints []HyperVCheckpoint `json:"checkpoints,omitempty"`
}

// HyperVDisk is one virtual hard disk attached to a guest.
type HyperVDisk struct {
	Path               string  `json:"path"`
	ControllerType     string  `json:"controller_type,omitempty"` // IDE / SCSI
	ControllerNumber   int     `json:"controller_number,omitempty"`
	ControllerLocation int     `json:"controller_location,omitempty"`
	FileSizeGB         float64 `json:"file_size_gb,omitempty"` // 实际占用（VHD 文件大小）
}

// HyperVNic is one virtual network adapter of a guest.
type HyperVNic struct {
	Name        string   `json:"name"`
	MAC         string   `json:"mac,omitempty"`
	Switch      string   `json:"switch,omitempty"`
	Status      string   `json:"status,omitempty"` // Ok / Degraded / ...
	Connected   bool     `json:"connected,omitempty"`
	IPAddresses []string `json:"ip_addresses,omitempty"`
}

// HyperVCheckpoint is one checkpoint (snapshot) of a guest.
type HyperVCheckpoint struct {
	Name       string `json:"name"`
	Created    string `json:"created,omitempty"` // RFC3339-ish
	ParentName string `json:"parent,omitempty"`
}

// ContainerInfo is one Docker/Podman container on a managed host.
type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	Status  string `json:"status,omitempty"` // Up / Exited / created …
	State   string `json:"state,omitempty"`  // running / exited / …
	Ports   string `json:"ports,omitempty"`
	Created string `json:"created,omitempty"`
	Runtime string `json:"runtime,omitempty"` // docker | podman
	// ComposeProject / ComposeService come from standard Compose labels when present.
	ComposeProject string `json:"compose_project,omitempty"`
	ComposeService string `json:"compose_service,omitempty"`
}

// ContainerReport is the payload agents POST for host container inventory.
type ContainerReport struct {
	HostID      string          `json:"host_id"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Timestamp   int64           `json:"timestamp"`
	HostName    string          `json:"host_name,omitempty"`
	Error       string          `json:"error,omitempty"`
	Runtime     string          `json:"runtime,omitempty"`
	Containers  []ContainerInfo `json:"containers"`
}

// HyperVReport is the payload agents POST for the Hyper-V guest inventory of one
// physical host. Error carries a collection failure (e.g. Get-VM unavailable) so
// the server can surface it without overwriting the last good inventory.
type HyperVReport struct {
	HostID      string `json:"host_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Timestamp   int64  `json:"timestamp"`
	HostName    string `json:"host_name,omitempty"`
	Error       string `json:"error,omitempty"`
	// 物理宿主机自身的内存（MB）。用于在虚拟机页的宿主机名后显示「可用/总内存」，
	// 直观反映宿主机资源余量。0 表示未采到（老 Agent / 采集失败时不显示）。
	HostTotalMemMB float64       `json:"host_total_mem_mb,omitempty"`
	HostAvailMemMB float64       `json:"host_avail_mem_mb,omitempty"`
	Guests         []HyperVGuest `json:"guests"`
}

// ============================================================================
// NetFlow / 五元组包采集结构体
// ============================================================================

// FlowRecord is one aggregated flow (five-tuple + bytes/packets).
type FlowRecord struct {
	SrcIP     string `json:"src_ip"`
	DstIP     string `json:"dst_ip"`
	SrcPort   uint16 `json:"src_port"`
	DstPort   uint16 `json:"dst_port"`
	Protocol  uint8  `json:"protocol"`
	Bytes     uint64 `json:"bytes"`
	Packets   uint64 `json:"packets"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	TCPFlags  uint8  `json:"tcp_flags"`
	SrcAS     uint32 `json:"src_as,omitempty"`
	DstAS     uint32 `json:"dst_as,omitempty"`
	InputIf   uint32 `json:"input_if,omitempty"`
	OutputIf  uint32 `json:"output_if,omitempty"`
}

type NetFlowStats struct {
	TotalFlows     int    `json:"total_flows"`
	TotalBytes     uint64 `json:"total_bytes"`
	TotalPackets   uint64 `json:"total_packets"`
	DroppedPackets uint64 `json:"dropped_packets"`
	Sampled        bool   `json:"sampled,omitempty"`
}

// NetFlowReport is the payload agents POST for NetFlow/packet aggregated flows.
type NetFlowReport struct {
	HostID      string       `json:"host_id"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Source      string       `json:"source"` // "netflow" | "packet"
	Timestamp   int64        `json:"timestamp"`
	WindowSec   int          `json:"window_sec"`
	Flows       []FlowRecord `json:"flows"`
	Stats       NetFlowStats `json:"stats"`
}

// ============================================================================
// SNMP 采集结构体（agent 周期轮询 IF-MIB + 系统组）
//
// 与 NetFlow 的关键区别：NetFlow 是设备 PUSH 的五元组流；SNMP 是 agent 主动 PULL
// 的接口计数器。计数器单调递增（可能 32 位回绕），速率/利用率由 agent 在两次轮询间
// 算好当 gauge 上报（RateValid 标注是否可信），原始 HC 计数器一并带上供 PG 取证。
// 粒度对齐 HardwareSnapshot：Error 非空时 server 不覆盖上一份好数据。
// ============================================================================

// SNMPInterface 是一张接口一轮的采集结果。
type SNMPInterface struct {
	Index       uint32 `json:"index"`                  // ifIndex
	Name        string `json:"name,omitempty"`         // ifName（做 VM label 首选，稳定）
	Descr       string `json:"descr,omitempty"`        // ifDescr
	Alias       string `json:"alias,omitempty"`        // ifAlias（运维口述描述）
	Type        int    `json:"type,omitempty"`         // ifType(IANAifType)
	MAC         string `json:"mac,omitempty"`          // ifPhysAddress
	SpeedBps    uint64 `json:"speed_bps,omitempty"`    // ifHighSpeed*1e6 优先，否则 ifSpeed
	AdminStatus int    `json:"admin_status,omitempty"` // 1 up 2 down 3 testing
	OperStatus  int    `json:"oper_status,omitempty"`  // 1 up 2 down 3 testing 4 unknown 5 dormant 6 notPresent 7 lowerLayerDown
	OperUp      bool   `json:"oper_up"`                // 归一：operStatus==1（告警 up/down 直接用）

	// agent 端算好的速率（gauge，给 VM 写时序 + 告警评估）
	InBps          float64 `json:"in_bps"`
	OutBps         float64 `json:"out_bps"`
	InPps          float64 `json:"in_pps,omitempty"`
	OutPps         float64 `json:"out_pps,omitempty"`
	InUtilPercent  float64 `json:"in_util_percent,omitempty"`
	OutUtilPercent float64 `json:"out_util_percent,omitempty"`
	InErrPps       float64 `json:"in_err_pps,omitempty"`
	OutErrPps      float64 `json:"out_err_pps,omitempty"`
	InDiscardPps   float64 `json:"in_discard_pps,omitempty"`
	OutDiscardPps  float64 `json:"out_discard_pps,omitempty"`

	// 最新原始计数器（给 PG 明细/取证；不推 VM）
	InOctets  uint64 `json:"in_octets,omitempty"`
	OutOctets uint64 `json:"out_octets,omitempty"`
	InErrors  uint64 `json:"in_errors,omitempty"`
	OutErrors uint64 `json:"out_errors,omitempty"`
	Counter64 bool   `json:"counter64,omitempty"` // 是否用了 HC 计数器
	RateValid bool   `json:"rate_valid"`          // 首轮/回绕/复位时为 false（速率不可信）
}

// SNMPSystem 是设备系统组（sysDescr/sysUpTime/sysName…）。
type SNMPSystem struct {
	Descr     string  `json:"descr,omitempty"`
	ObjectID  string  `json:"object_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Location  string  `json:"location,omitempty"`
	UptimeSec float64 `json:"uptime_sec,omitempty"` // sysUpTime/100
}

// SNMPSnapshot 是一个被轮询设备一轮的整体快照（一台设备一份）。
// 服务端以 TargetIP 为稳定身份做改名迁移，再按 (host_id, device_name) upsert。
type SNMPSnapshot struct {
	TargetName  string          `json:"target_name"`
	TargetIP    string          `json:"target_ip"`
	Timestamp   int64           `json:"timestamp"`       // Unix 秒（server push VM 时 *1000）
	Version     string          `json:"version"`         // "2c" | "3"
	IntervalSec int             `json:"interval_sec"`    // 本轮速率的 delta 基准
	Reachable   bool            `json:"reachable"`       // 是否成功采到
	Error       string          `json:"error,omitempty"` // 采集失败原因（失败时 server 不覆盖上次好数据）
	System      SNMPSystem      `json:"system"`
	Interfaces  []SNMPInterface `json:"interfaces"`
}

// SNMPReport 是 agent 周期上报的 SNMP 载荷（POST /api/v1/agent/snmp）。
type SNMPReport struct {
	HostID      string         `json:"host_id"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Timestamp   int64          `json:"timestamp"`
	Snapshots   []SNMPSnapshot `json:"snapshots"` // 每轮通常一台设备一条
}

// ============================================================================
// SNMP Trap 事件结构体（agent 监听 :162，v1+v2c）
// ============================================================================

// SNMPVarbind 是 trap 里一条 name=value（value 统一 stringify，JSON 安全）。
type SNMPVarbind struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`  // "OctetString"/"Integer"/"Counter32"/"TimeTicks"/"IpAddress"/"OID"/...
	Value string `json:"value"` // 数值型十进制字符串，OctetString 尽量可读
}

// SNMPTrapEvent 是归一后的一条 trap 事件。
type SNMPTrapEvent struct {
	SourceIP     string        `json:"source_ip"`
	Version      string        `json:"version"` // "1" | "2c"
	Community    string        `json:"community,omitempty"`
	TrapOID      string        `json:"trap_oid"`             // v1 按 RFC3584 归一
	Severity     string        `json:"severity"`             // info/warning/critical（启发式）
	UptimeSec    float64       `json:"uptime_sec,omitempty"` // sysUpTime/100
	Timestamp    int64         `json:"timestamp"`            // 接收时刻 Unix 秒
	Enterprise   string        `json:"enterprise,omitempty"` // v1
	AgentAddr    string        `json:"agent_addr,omitempty"` // v1
	GenericTrap  int           `json:"generic_trap,omitempty"`
	SpecificTrap int           `json:"specific_trap,omitempty"`
	Varbinds     []SNMPVarbind `json:"varbinds,omitempty"`
}

// SNMPTrapReport 是 trap 批量上报载荷（POST /api/v1/agent/snmp/trap）。
type SNMPTrapReport struct {
	HostID      string          `json:"host_id"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Timestamp   int64           `json:"timestamp"`
	Traps       []SNMPTrapEvent `json:"traps"`
}

// ============================================================================
// SNI/DNS 域名观测（agent 抓包提取「目的 IP ↔ 真实域名」，明文，不解密内容）
// ============================================================================

// DNSMapEntry 是一条「IP → 域名」观测：来自 DNS 应答的 A/AAAA 记录，或 TLS ClientHello 的 SNI。
type DNSMapEntry struct {
	IP     string `json:"ip"`
	Domain string `json:"domain"`
	Source string `json:"source"` // "dns" | "sni"
}

// DNSMapReport 是 agent 周期上报的域名观测（POST /api/v1/agent/dnsmap）。
type DNSMapReport struct {
	HostID      string        `json:"host_id"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	Timestamp   int64         `json:"timestamp"`
	Entries     []DNSMapEntry `json:"entries"`
}

// ContentAuditEvent 是一条明文 HTTP 交换或 HTTPS 元数据审计观测。
// ⚠ 高敏感：Body/RespBody 可能含 prompt、completion 与 PII。Agent 默认在上报前
// 执行 redacted 策略；只有显式 body_mode=full 才保留未经脱敏的正文。
type ContentAuditEvent struct {
	SrcIP    string `json:"src_ip"`
	DstIP    string `json:"dst_ip"`
	DstPort  uint16 `json:"dst_port"`
	Protocol string `json:"protocol,omitempty"` // http | tls | gateway
	Method   string `json:"method"`             // GET/POST/...
	Host     string `json:"host,omitempty"`     // Host 头
	Path     string `json:"path,omitempty"`     // 请求路径（如 /v1/chat/completions、/api/chat）
	CType    string `json:"ctype,omitempty"`    // 请求 Content-Type
	Body     string `json:"body,omitempty"`     // 请求体（增量2:TCP流重组后的完整 body，截断上限内）
	// ---- 增量 2：响应(completion)。TCP 流重组后回填 ----
	Status          int      `json:"status,omitempty"`          // 响应状态码
	RespCType       string   `json:"resp_ctype,omitempty"`      // 响应 Content-Type
	RespBody        string   `json:"resp_body,omitempty"`       // 响应体全文（含大模型 completion；SSE 为拼接后的数据流）
	ReqTruncated    bool     `json:"req_truncated,omitempty"`   // 请求体达上限被截断
	RespTruncated   bool     `json:"resp_truncated,omitempty"`  // 响应体达上限被截断
	CaptureBackend  string   `json:"capture_backend,omitempty"` // native | tshark | gateway
	BodyMode        string   `json:"body_mode,omitempty"`       // metadata | redacted | full
	ReqBytes        int      `json:"req_bytes,omitempty"`       // 策略处理前的可见请求正文大小
	RespBytes       int      `json:"resp_bytes,omitempty"`      // 策略处理前的可见响应正文大小
	ReqSHA256       string   `json:"req_sha256,omitempty"`      // 原始可见请求正文哈希（关联分析）
	RespSHA256      string   `json:"resp_sha256,omitempty"`     // 原始可见响应正文哈希
	RedactionCount  int      `json:"redaction_count,omitempty"`
	RedactionLabels []string `json:"redaction_labels,omitempty"` // Agent 端已发现并遮蔽的类别
	// Gateway/SDK 结构化治理字段；被动抓包可留空，应用层集成应尽量提供。
	PrincipalID    string   `json:"principal_id,omitempty"`
	ApplicationID  string   `json:"application_id,omitempty"`
	EventID        string   `json:"event_id,omitempty"` // integration retry idempotency key
	RequestID      string   `json:"request_id,omitempty"`
	TraceID        string   `json:"trace_id,omitempty"`
	LLMProvider    string   `json:"llm_provider,omitempty"`
	LLMModel       string   `json:"llm_model,omitempty"`
	LLMOperation   string   `json:"llm_operation,omitempty"`
	LLMStream      bool     `json:"llm_stream,omitempty"`
	InputTokens    int      `json:"input_tokens,omitempty"`
	OutputTokens   int      `json:"output_tokens,omitempty"`
	ToolCalls      int      `json:"tool_calls,omitempty"`
	LatencyMS      int64    `json:"latency_ms,omitempty"`
	PolicyDecision string   `json:"policy_decision,omitempty"`
	RiskLabels     []string `json:"risk_labels,omitempty"`
	Ts             int64    `json:"ts"` // 观测时刻 Unix 秒
}

// ContentAuditReport 是 agent 周期上报的内容审计载荷（POST /api/v1/agent/content-audit）。
type ContentAuditReport struct {
	HostID      string              `json:"host_id"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	Timestamp   int64               `json:"timestamp"`
	Events      []ContentAuditEvent `json:"events"`
}

// ---- 分布式多点探测（迭代 D）：agent 作为多地探针执行 HTTP 探测 ----

// ProbeTask 是服务端在上报响应里下发给 agent 的一个探测任务（对应一个被标为「分布式」的 API 接口）。
type ProbeTask struct {
	ID           string            `json:"id"` // = APIEndpoint.ID
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Method       string            `json:"method,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
	ExpectStatus int               `json:"expect_status,omitempty"`
	TimeoutSec   int               `json:"timeout_sec,omitempty"`
}

// ReportResponse 是 /api/v1/agent/report 的响应体：除认回的规范 host_id 外，可携带分布式探测任务。
type ReportResponse struct {
	Status     string      `json:"status"`
	HostID     string      `json:"host_id,omitempty"`
	ProbeTasks []ProbeTask `json:"probe_tasks,omitempty"`
}

// ProbeResult 是 agent 执行一个探测任务的结果。
type ProbeResult struct {
	TaskID    string  `json:"task_id"`
	OK        bool    `json:"ok"`
	LatencyMs float64 `json:"latency_ms"`
	Code      int     `json:"code"`
	Msg       string  `json:"msg,omitempty"`
	Ts        int64   `json:"ts"`
}

// ProbeResultReport 是 agent 回报的一批探测结果（POST /api/v1/agent/probe-results）；
// 服务端据 HostID 把每个 agent 归为一个「探测点/地域」，聚合出区域性 vs 全局故障。
type ProbeResultReport struct {
	HostID      string        `json:"host_id"`
	Hostname    string        `json:"hostname,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	Results     []ProbeResult `json:"results"`
}

// LabeledSample 是一条带标签的时序样本（指标名 + 标签 + 值 + 毫秒时间戳）。用于摄入 Prometheus
// 生态指标（exporter 抓取 / remote_write）——相比固定 struct 的主机指标或扁平 map 的插件 gauge，
// 它保留 Prometheus 的多标签语义（如 mysql_global_status_threads{instance,...}），VictoriaMetrics
// 原生按标签存储，与主机/拨测指标同库可联合查询。
type LabeledSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	TsMs   int64             `json:"ts_ms"` // Unix 毫秒（Prometheus 惯例）
}
