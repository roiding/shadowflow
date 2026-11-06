# ShadowFlow 代理池接入与预热门槛

更新：2026-11-05。本项目消费独立 `proxy_pool` 的 `/api/v1` 协议，不负责免费列表抓取、注册、购买或转发业务。代理池只验证连接及目标 TLS，实际行情响应仍由 ShadowFlow 获取和校验。

## 1. 先预热，再采集

```text
提前调度 / 正式批次启动
  -> 按目标和请求量动态计算目标 POST /api/v1/prewarm（默认上限 cap=8，动态计算小批目标）
  -> 接收 202 和 job_id（此时还不能开始采集）
  -> GET /api/v1/jobs/{id} 等待任务终态
  -> COMPLETED 且 ready >= requested，或 PARTIAL 有完成时间且 ready >= minimum 放行
  -> 四个全局代理 worker 分别领取 sticky 租约
  -> 请求行情；健康复用/续租，失败换代理重试原请求
```

`SHADOWFLOW_PROXY_POOL_PREWARM_COUNT` 保留名称，但定义为每轮预热目标的上限（默认 8，范围 1..100），不再作为固定启动门槛；不论配置为 100 还是 8，系统都不会无条件固定预热 100 个连接。

### 动态预热目标计算

Manager 根据单次任务请求数 N 与业务预期并发 C 动态选择活跃目标：
- active = min(N, C, 全局workers<=4, cap)（未知 N 时取 C）。
- 仅有 1 个请求时（N=1），仅预热 1 个连接（active=1, target=1）。
- 当任务超过 active 时，额外配置备用连接至多 ceil(active/2)：
  target = min(N（若已知）, active + ceil(active/2), cap)。
- 大批次 4 并发场景下，默认预热目标为 6（4 active + 2 备用），而非硬性 100。
- Ensure 自动单请求（无批量提示）默认目标为 1。
- 前台与调度提前预热在 N 未知时，按业务实际并发计算：
  - 成份串行采集：默认目标 2（1 active + 1 备用）；
  - 个股五分钟 K 线未指定批量（4 并发）：默认目标 6（4 active + 2 备用）；
  - 盘后资金归档内部也有并行采集，按全局 4 并发准备，默认目标 6；
  - 若已知实际缺失记录数：0 缺失不预热，1 => 1，2 => 2，4 => 4，5 => 5，>=6 => 6（受 cap 上限约束）。

### 终态等待与放行判定

等待远端任务到达终态；客户端处于 PENDING、QUEUED 或 RUNNING 时绝对不准开始采集：
- COMPLETED：要求 ready >= target（即达到 requested 目标）才放行采集；
- PARTIAL：必须为终态且带有明确完成时间 finished_at，在 ready >= minimum（本轮最低可开工容量，如主并发 active 或补充门槛 1）时允许放行；若 ready < minimum 则判定失败不放行；
- FAILED、CANCELLED、INTERRUPTED、超时或协议异常均判定失败不放行。

### 空池小批补充与并发收敛

同目标的并发调用通过 singleflight 共同等待同一个预热任务，不会让四个 worker 各自发起重复预热。一个等待者超时或取消不会终止其他等待者需要的任务；全部等待者离开或服务退出时才向远端发送取消。

运行过程中若遇可用池耗尽（空池）：
- 仅按当前缺口小批补充，不为每个 worker 启动作业，不重做整批 100；
- 补充预热目标计算为 target = max(1, 上次requested - 同目标未临期的本机有效租约数)，至多为上次需求小窗口（最多 6）；最低可开工容量 minimum = 1；
- 避免在其他 worker 已持有可用租约时强等 4 个全新代理；PARTIAL 终态且 ready >= minimum 同样允许放行。

### 关键语义说明

- 100 并非强制要求：预热 target 是远端总 Ready 目标而非额外增量；服务端报告的 Ready 达标即满足要求。
- Ready 定义：Ready 不包含处于冷却（CD）中的连接。
- Sticky 复用：成功领取的代理通过健康 sticky 会话机制复用与续租，批次内不为单次请求重新预热。
- 验证声明：本机制未经生产实网验证，系统不会假定或声称能够自动观测吞吐以估算代理寿命。

真实服务的待执行状态为 `PENDING`，客户端同时兼容 `QUEUED`，二者与 `RUNNING` 都只能继续等待。服务端可能在当前 Ready 超出申请数量时上调 `target`；客户端仍按本次原始 `requested` 与 `minimum` 判断，不把这个统计变化误判为协议失败。

完成记录不是库存预留，也不是长期存活保证：

- 正式成份、盘后、五分钟批次只复用本机收到完成结果后 30 秒内的记录；过旧则重做预热。提前任务过早完成，正式任务可能需要再预热一次，不把过期证据算成有效。
- 批次内不为每根 K 线重新预热；领取/续租仍由代理池按目标、来源、CD 和证据有效期检查。
- 盘中分钟榜和临时行情查询首次访问某目标也有预热门槛，后续依靠领取和续租，避免每分钟整批预热。
- 领取 HTTP 200 但 `delivered=0` 不算成功。每次逻辑 HTTP 请求至多触发一次补充预热，再领取；仍无供应则失败，不直连、不无限循环。
- `ready` 是任务最后报告时的数量，不是实时库存。不同目标的 Ready 不能相加作为通用可用代理数。

## 2. 交易日提前计划

仅在配置代理池并启用调度时开启；原业务时间和重试规则不变。

| 业务 | 提前预热 | 原业务时间 |
| --- | --- | --- |
| 行业、概念目录与成份同步 | 06:55、07:25、07:55 | 07:00、07:30、08:00 |
| 盘中分钟板块榜 | 09:26、12:56 | 09:31、13:01 起 |
| 盘后完整归档及日终行情 | 15:55 | 16:00 起 |
| 个股五分钟 K | 16:10、17:25、19:55 | 16:15、17:30、20:00 |

时区为 Asia/Shanghai。提前任务为持久化 `proxy-prewarm-*` 类型，使用独立 lane，预算八分钟、最多两次尝试。16:10 的预热不会覆盖原来的归档重试。禁用业务同时禁用对应预热；对应成份、日终或个股五分钟数据已完整时，也跳过提前预热，避免无效消耗。重启只补仍处于十分钟启动窗口的提前任务，不补跑当天全部过期预热。成份同步始终受 09:15 截止时间约束，包括预热等待，不能下午补跑上午成份。

提前任务缺失、失败、服务晚启动或手工执行 `collect` 都不绕过门槛：正式业务入口自行准备所需目标。供应不足保留原有待补数据；完整归档和成份关系不会被半成品覆盖。纯清理、维护和封存已完整数据不需要预热。

## 3. 目标范围

目标从实际配置的 endpoint 推导。

| 业务 | 默认预热目标 |
| --- | --- |
| 目录与成份、日终报价 | `push2.eastmoney.com:443`、`push2delay.eastmoney.com:443` |
| 个股五分钟 K | `push2his.eastmoney.com:443` |
| 暗盘榜、五分钟资金曲线 | `quotederivates.eastmoney.com:443` |

HTTPS 使用 `validation: {"mode":"tls","target":"host:port"}`，不同目标分别完成预热。HTTP 开发环境使用 `basic`，不能将 basic 证明冒充 TLS。正常验证证书，不把代理池 Key 发给行情服务器。

五分钟正式采集只走 `/api/qt/stock/kline/get?klt=5&fqt=0`，完整校验当日 48 根后写入，不再优先 `trends2` 或失败后合成一分钟数据。日终行情目前仍通过既有 `clist/ulist` 快照补充 OHLC，本次未改成全市场历史 `klt=101` 回填。历史日 K 实验和离线 `recover_intraday.py` 是独立路径，本次未把该脚本改成生产代理采集器。

## 4. 四个 worker 与租约

所有共享 Go upstream client 的行情、成份及资金请求共用最多四个代理 worker，不是每个周期各四个。`SHADOWFLOW_UPSTREAM_MAX_CONCURRENCY` 可以调低，代理模式上限为四。每个 worker 按目标维护带进程随机前缀的独立 `session_id`；同一逻辑请求的重试留在同一 worker。借用 worker 时优先选择已有同目标健康绑定的空闲 worker，租约过期但绑定仍有效时可以续租，避免串行请求轮流占用四个连接。

领取示例：

```json
{
  "count": 1,
  "timeout": 30,
  "cd": 300,
  "mode": "sticky",
  "session_id": "<本进程、worker、验证范围的稳定标识>",
  "protocols": ["http", "socks5"],
  "source_ids": ["<获准的渠道 ID>"],
  "validation": {"mode":"tls","target":"push2his.eastmoney.com:443"}
}
```

- 有效租约内复用连接身份；剩余时间不足本次业务超时加余量（至少五秒）时释放旧租约，用原会话领取新租约。检查 `session_changed`、绑定版本及连接信息；健康续租不删除绑定。绑定或供应商期限临近时正常换绑，不记录代理失败。
- 业务截止时间取单次超时、调用方截止时间及租约/绑定/已知供应商期限的最小值，并为租约截止保留余量。
- 失败发送 `/leases/{id}/feedback`，释放租约，`DELETE /sessions/{id}`，再领取并重试原 URL。股票、日期、周期不变；失败绑定未确认释放时停止本轮，不继续使用它。
- CD 由服务端按项目与真实连接执行；客户端不换 Key、清 CD 或缩短旧窗口。同一请求还排除已失败的完整连接指纹。
- 租约、验证证据、逻辑绑定、供应商期限是不同概念；公开代理可能随时死亡，同一连接身份也不能证明固定出口 IP。
- 退出释放已知租约和本进程绑定；取消不记作坏代理。清理失败有日志及计数，不能记录为释放成功。

## 5. 失败与数据质量

网络/TLS/读体中断、HTTP 200 空正文、行情非 JSON、代理鉴权或可重试 HTTP 拒绝触发反馈及换代理。遵守 `Retry-After`，包括后续新请求。控制故障或没有可领取资源不能被描述成已证实的东财风控。

因租约到期而主动截断的请求只释放过期绑定并重试，不把本地时间预算耗尽反馈成坏代理。

合法 JSON 的业务 `data:null`、错误码、停牌或日期无行情由数据源层处理，不自动污损代理健康。原有日期、时间点、数量、数值及 OHLC 校验不放宽。传输层响应上限为 8 MiB，K 线解析器保留自己的更小上限。

一次逻辑 HTTP 请求默认最多三轮代理尝试；数据源和调度原有的有界重试仍然存在。持续不足返回批次失败/部分完成，由原有补采续接，不无限等待成功。

## 6. 配置与迁移

```sh
export SHADOWFLOW_PROXY_POOL_URL='https://proxy-pool.522401.xyz'
export SHADOWFLOW_PROXY_POOL_TOKEN='<本项目专用 Key>'
export SHADOWFLOW_PROXY_POOL_SOURCE_IDS='proxyscrape-elite,spys-http'
export SHADOWFLOW_PROXY_POOL_PREWARM_COUNT=8
export SHADOWFLOW_PROXY_POOL_PREWARM_TIMEOUT_SECONDS=120
export SHADOWFLOW_PROXY_POOL_ACQUIRE_TIMEOUT_SECONDS=30
export SHADOWFLOW_PROXY_POOL_COOLDOWN_SECONDS=300
export SHADOWFLOW_REQUEST_TIMEOUT_SECONDS=12
```

URL 可以是服务根地址或 `/api/v1`，不能继续填写旧 GET 列表地址。控制请求独立直连，不依赖池内代理，并拒绝重定向。Token 应为获准项目 Key；来源留空会继承项目权限，部署前应限制该项目可用来源，避免无意混入收费或有额度来源。`SHADOWFLOW_PROXY_POOL_PREWARM_COUNT` 设置每轮预热目标的上限（默认 8，范围 1..100），系统按实际请求与并发动态确定小批目标，不再固定预热 100。

旧 `REFRESH_SECONDS`、`FAILURE_LIMIT`、`STRATEGY`、`MAX_PROXIES` 仍可被旧配置解析器读取，但不控制新版租约路径。新路径固定健康 sticky 复用，按任务预热而非周期 GET 列表。`DIRECT_FALLBACK=true` 在新客户端初始化时明确报错。仓库中的旧列表解析辅助代码不再是 server/collect 的入口。

服务启动只校验配置，不等待远程代理池在线；只有外部数据任务等待预热。此前 SSH 控制隧道实验成功不代表生产公网控制 API 已稳定，部署前须从实际采集机器检查。

## 7. 状态与验收

`GET /api/v1/system/status` 的 `proxy_pool` 提供：

- `prewarms[]`：包含目标、job_id、状态、`requested`（申请目标）与新增的 `minimum`（本轮最低可开工容量）、最后报告的 Ready/失败数及完成时间。
- `workers/busy_workers`：全局代理 worker 数量。
- `lease_acquires/lease_renewals/session_changes`：领取、健康续租、绑定变化。
- `cleanup_errors`：释放/反馈失败，不包含代理地址、密码或完整代理 URI。
- 旧 `proxy_count/available_count/expired_count` 在新版表示本机租约状态，不是远端候选库存；`refresh_*` 对应预热结果。

`/metrics` 另暴露 worker、租约和清理错误计数。日志不记录控制密钥或返回的凭据正文。

本地测试覆盖等待真实完成、同目标合并、目标隔离、部分预热放行与拒绝门槛、续租、失败重试同请求、四并发、取消、空池有界小批补充、正常无数据不误判及成份不发布半成品。100 不是强制要求，默认按小批动态计算目标（如 4 并发目标 6，成份串行目标 2）；实际免费代理可用性、四并发长期成功率和 N1 部署效果仍需真实验收；小样本报告见 `eastmoney-proxy-probe-2026-11-05/report.md`，不能把模拟测试当作新的实网结论。
