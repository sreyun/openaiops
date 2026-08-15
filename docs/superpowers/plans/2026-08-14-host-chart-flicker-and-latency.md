# 主机指标曲线「一会有一会没有 + 加载慢」：根因与修复

现象：主机详情的指标曲线时有时无，出现时也常常要等很久；有时只画出很短一段，前端标注
「仅覆盖 x / y」。

结论：**这不是一个 bug，是三个独立缺陷叠加**，且都在 VictoriaMetrics 读取链路上。三者互相
放大，所以表现得像随机抖动。

---

## 一、读写共用一个熔断器 —— 「一会有一会没有」的直接原因

`vmWriter` 只有一个 `breaker`，而 `doVMRequest` 同时服务两类流量：

| 流量 | 端点 | 频率 |
|---|---|---|
| 写入 | `POST /api/v1/import/prometheus` | 每台主机每个采集周期，持续不断 |
| 读取 | `GET /api/v1/query_range`、`/query`、`/export`、`/label/…/values` | 用户打开图表时 |

`newVMCircuitBreaker` 的默认值是「连续 5 次失败 → 打开 30 秒」。写入路径任何一次
4xx/5xx——一条标签不合法、VM 短暂过载、磁盘写满——都会计数；连续 5 次就把熔断器打开。

而这 30 秒里**所有查询**都拿到 `errVMCircuitOpen`，`loadDurableHostHistory` 于是静默退回
内存环。内存环只有 raw 1200 / 1m 2880 / 5m 8640 个点，24h、7d 这种窗口立刻缩水，前端画出来
就是「仅覆盖 x/y」甚至空图。

写入频率远高于读取，所以从用户角度看就是**曲线在随机地时有时无**。

`push()` 里还有一行显式的 `v.breaker.failure()`（处理 4xx，因为 `doVMRequest` 只对 5xx 计
数），进一步加快了写入把读取拖下水的速度。

**修复**：`vmWriter` 拆成 `breaker`（写）与 `readBreaker`（读），`doVMRequest` 只管写入，
新增 `doVMQuery` 管所有查询。8 个读取调用点全部切到 `doVMQuery`。

顺带去掉 `doVMRequest` 里 `if v.breaker == nil { v.breaker = newVMCircuitBreaker() }` 这段
惰性初始化——它在共享字段上做「判空再赋值」，而写入 flush 协程和图表读取是并发的，这是一个
数据竞争，两个协程各建一个熔断器还会把状态悄悄劈成两半。`newVMWriter` 本来就总会赋值。

## 二、「半开」实际是「全开」 —— 「要转很久」的原因

```go
if !b.openUntil.IsZero() && now.After(b.openUntil) {
    b.halfOpenProbe = true      // 写了，但从来没有任何地方读它
    b.openUntil = time.Time{}   // 直接清零 → 之后所有调用一律放行
}
```

`halfOpenProbe` 这个字段**从头到尾没被读过**。冷却期一满，`openUntil` 就被清零，于是不是
「放一个探针」，而是「全部放行」。

后果：VM 真的挂掉或变慢时，每过 30 秒就有一整批并发请求同时放出去，各自卡满
`vmQueryTimeout()`（默认 15 秒）才失败回退到内存。用户体验就是**图有时候要转十几秒才出来**。

**修复**：`openUntil` 保持有值直到探针有结果；`allow()` 在冷却期满后只放行第一个调用者，
其余立即拿到熔断错误并瞬间走内存回退。探针失败直接回到 open（不再重新数到阈值，否则又会
先放出一整批）。

## 三、读取路径完全没有缓存 —— 慢的另一半

每次刷新都对 VM 重发一条

```
{__name__=~"aiops_cpu_percent|aiops_cpu_cores|…（57 个）",host="…",path!~".*(overlay2|/docker/|/kubelet/pods|containerd).*"}
```

的 `query_range`。这个选择器没有任何精确的 `__name__`，VM 必须扫元数据索引，再把每个磁盘
分区、每块 GPU、每个 `(proto,state)` 展开成独立序列。24h/7d 窗口下这是几百毫秒到数秒级的
查询，而前端每次刷新原样重来一遍。除主机图表外，AI 证据、预测拟合、SLO 读取也走同一条路径。

**修复**：`vm_history_cache.go` —— 只缓存 VM 那一半。

关键点是**缓存不会让曲线变旧**：`loadDurableHostHistory` 永远把内存环最近 15 分钟叠在 VM
结果之上（`recentHistoryTail` + `spliceHistory`），最新的点始终来自内存。所以只要 TTL 明显
小于 `memHistoryOverlaySec`（15 分钟），用户看到的尾部就一直是实时的。TTL 取查询自身的
step，夹在 [15s, 120s]：7d 图一个点就是 1260 秒，几秒钟重查一次毫无意义。

缓存键必须**同时**对 `from` 和 `to` 按 TTL 取整。相对窗口（`from=now-24h, to=now`）两端每秒
都在变，不取整的话每次请求都是新键、命中率恒为零。取整带来的偏移最多一个 TTL，而这段永远
落在内存叠加窗口里，对图形不可见。

零长度结果不入缓存——否则一次瞬时失败会把空图钉住一整个 TTL。`get` 返回切片副本，避免调用方
往缓存条目上 append。

## 四、降级不可见 —— 为什么这件事这么难查

VM 读失败时，`loadDurableHostHistory` 返回内存数据、`hostOK=true`，接口照样 **200**。
从外面看，「VM 挂了」和「这台主机本来就没数据」完全一样。

**修复**：`loadDurableHostHistorySource` 额外返回 provenance，`handleHostHistory` 写进响应头
`X-AIOps-History-Source: vm+ram | ram | ram-fallback`（响应体仍是裸数组，经典控制台对它做
`Array.isArray`，不能改形状），并按 60 秒限流打一条 warn 日志。

---

## 验证

| 测试 | 断言 |
|---|---|
| `TestVMWriterKeepsReadAndWriteBreakersSeparate` | 两个熔断器不是同一个；打爆写熔断器后读仍放行 |
| `TestVMCircuitBreakerHalfOpenAdmitsOneProbe` | 冷却后只放行一个探针；探针失败直接回 open；探针成功彻底闭合 |
| `TestVMHistoryCacheKeyIsStableWithinItsTTLBucket` | 滑动窗口在一个桶内键不变、跨桶必变；按主机与指标子集分隔 |
| `TestVMHistoryCacheTTLStaysUnderTheRAMOverlay` | 任意 step 下 TTL 都在 [15s,120s] 且 < 15 分钟叠加窗口 |
| `TestVMHistoryCacheGetPutAndExpiry` | 过期淘汰；空结果不缓存；返回副本不可被调用方改坏 |
| `TestVMHistoryCacheEvictsWhenFull` | 条目数不超过上限 |

## 还没做的（下一个杠杆）

**并发同键查询没有做 single-flight。** 缓存把稳态请求量压下去了，但在桶轮转的瞬间，所有正在
看图的客户端会同时未命中，对 VM 发出若干条完全相同的重查询。加一个 single-flight 可以把它
压到 1 条。没有一并做是因为它引入「一个卡住的查询阻塞其他人」这一新失败模式，需要单独设计
超时与逃生路径，值得单独一轮改动。
