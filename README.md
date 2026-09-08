# ShadowFlow 暗流

面向行业、概念和个股资金趋势研究的单机 Web 系统。Go 服务在交易日 `09:31-11:30`、`13:01-15:00` 每分钟采集完整板块榜，仅供盘中实时展示。每天 `16:00` 从东方财富 `darktrade` 与 `darktradetick` 独立重新抓取行业、概念、个股完整日终榜及 48 个五分钟累计资金点；`16:15` 起抓取有成交个股的 48 根未复权五分钟 K。完整日归档直接按交易日长期累积，每日只保存一份主数据和一条轻量完整性摘要；日级特征及后续 `1/3/5/10/20` 日收益标签均从主归档生成。盘中工作数据只在长期数据全部完整后由次日 `09:00` 任务清理。

东方财富数据源资料见 [项目记忆](项目记忆.md)。`push2/push2his` 的请求参数、Cookie 状态窗口、重试策略和故障判定统一以 [行情接口调用规范](docs/EASTMONEY_PUSH2_API.md) 为准。

## 本地开发

本地开发需要 Go 1.25+、Node.js 22+ 和 npm。生产安全基线以 Dockerfile 的构建工具链和镜像扫描为准，不以本机安装版本判断。

```bash
cd backend
go run ./cmd/server
```

另开终端：

```bash
cd frontend
npm install
npm run dev
```

访问 [http://localhost:5173](http://localhost:5173)。Vite 会把 `/api` 和 `/health` 代理到 `127.0.0.1:8080`。

## 单进程运行

```bash
cd frontend
npm ci
npm run build

cd ../backend
SHADOWFLOW_STATIC_DIR=../frontend/dist go run ./cmd/server
```

访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)。服务默认只绑定本机；未配置 Token 时本地 API 可直接访问。API 规范见 [backend/openapi.yaml](backend/openapi.yaml)，Prometheus 指标位于 `/metrics`。

鉴权由 `SHADOWFLOW_API_TOKEN` 是否为空控制：为空时关闭，有值时开启。开启后，除 `/health/live`、`/health/ready` 和静态页面外，`/api/v1/*` 与 `/metrics` 都需要 Bearer Token。Token 至少 16 个字符。注意：Docker 部署时容器内绑定 `0.0.0.0`，`compose.yaml` 因此只把宿主机端口发布在 `127.0.0.1`；若未配置 Token 就把端口发布到非回环地址，所有数据与 `/metrics` 将无鉴权暴露到网络（Docker 的 iptables 规则还会绕过宿主机防火墙）。带 Token 调用示例：

```bash
curl -H "Authorization: Bearer $SHADOWFLOW_API_TOKEN" \
  'http://127.0.0.1:8080/api/v1/ranks/latest?type=industry'
```

前端会在收到 401 时显示 Token Gate；Token 只保存在当前浏览器标签页的 `sessionStorage`。导出链接会通过带 Token 的前端请求下载，不应把 Token 拼进 URL。

## 数据修补

- **整日资金重采**：`./collect -task end-of-day -date YYYY-MM-DD`（生产同路径，含限速/熔断/质量核算；上游仅保留约 5 个交易日）。成功后还需运行 `./collect -task stock-kline -date YYYY-MM-DD` 补齐行情并封存；资金任务成功不等于整个归档已完成。
- **个别盘中分钟缺失**：`backend/cmd/backfill_minutes`（`--trade-date`/`--clocks` 参数化；collect → insert → verify 三步，payload 有错误时 insert 会拒绝执行）。
- 历史上的 `backfill_stock` / `backfill_concept` 已删除，资金修补统一走 `collect -task end-of-day`，个股行情及封存走 `collect -task stock-kline`。

分批资金采集先写隔离暂存表，只有候选代码全集均具备 48 点时，才在单个事务内替换对应主归档并更新质量清单。中途失败或发布事务失败不会覆盖旧归档；完成/失败运行的暂存数据会清理，过期运行由维护任务回收。成功替换个股资金后，旧 K 线会失效并等待上述行情任务补齐。

日终数据接口：

```bash
curl 'http://localhost:8080/api/v1/ranks/daily-close?type=industry&trade_date=2026-08-13'
curl 'http://localhost:8080/api/v1/ranks/daily-close?type=concept&trade_date=2026-08-13'
curl 'http://localhost:8080/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-13'
curl -o daily-close.csv 'http://localhost:8080/api/v1/research/daily-close/export?trade_date=2026-08-13'
curl 'http://localhost:8080/api/v1/stocks/300308/boards?as_of=2026-08-13'
curl 'http://localhost:8080/api/v1/stocks/300308/research-5m?trade_date=2026-08-13'
curl 'http://localhost:8080/api/v1/boards/concept/BK1128/stocks?as_of=2026-08-13'
curl 'http://localhost:8080/api/v1/relations/changes?trade_date=2026-08-13'
```

## 今日监控：控盘度与排名变化

“今日监控”的行业/概念表在“排名”和“板块/概念”之间显示 **控盘变动**，不再单独设置移动栏或仅展示前十名。

- 首列“排名”仍是上游暗盘榜原始排名；新列比较的是控盘度排名，不能混用。
- 估算成交额 = `abs(dark_money) / dark_activity`；活跃度使用接口原始小数。
- 控盘度 = `main_money_inflow / 估算成交额 × 100`，保留两位小数，不带 `%`。主力净流入已经含暗盘，不再叠加一次暗盘资金。
- 今日最新完整截面与前一个交易日的完整收盘截面，分别按控盘度降序排名。行业、概念分开计算，两日均采用上述估算口径；不影响动态筛选的原有成交额/系数计算。
- 排名变化 = `昨日控盘排名 - 今日控盘排名`。上升显示红色 `↑3`，下降显示绿色 `↓2`，持平显示 `—`；无效数据或缺少昨日数据时显示 `--`。仅当昨日全榜可用且没有此代码时，显示“新入榜”。悬停可查看比较日期及两日名次。
- 排名使用未四舍五入的控盘度；完全相同值并列（如 `1、1、3`）。搜索、分页及其他列的排序不改变控盘排名，新列表头可按增减位数排序。
- `GET /api/v1/ranks/latest` 的 `meta.previous_trade_date` 使用现有交易日历，根据实际展示的快照日确定；不以自然日减一，也不跳过缺档日。周末、节假日或快照滞后时，页面明示比较日期。
- 前端按代码顺序读取 `daily-close` 的全部分页，校验日期、类型、总数、页长度及重复代码。任意页缺失或失败时不使用部分榜单计算排名。基线缺失时随自动刷新重试，也可点击“立即刷新”重试。

此次仅给最新榜单元数据补充前一交易日，排名计算在前端完成，不修改采集或存储。部署本功能需要同时更新前端和后端；旧后端未返回比较日期时前端显示 `--`。

## 动态连续筛选

前端“动态筛选”页允许分别为概念和个股添加或删除任意条件，选择“全部满足”或“任一满足”，并配置连续交易日数、是否仅主板、是否排除 ST、个股是否必须属于命中概念。内置初始模板就是原始策略：连续 3 日，概念成交额大于 500 亿元、个股成交额大于 2 亿元，换手率大于 3%，涨幅 1%–6%，控盘系数 1.5%–6%。控盘系数按 `(主力明盘 + 主力暗盘) / 成交额 × 100%` 即时计算。

筛选读取本系统已经原子归档的完整 `daily_close`，不请求东方财富概念历史 K 线。只有累积日期达到所选连续日数才会计算结果；不足时接口明确返回 `ready=false`，不会补值或推测。停牌个股不会使整日失效，但它本身因缺少可用行情而不会入选。

通用接口为 `POST /api/v1/focus/scan`。API 使用原始单位：资金/成交额为元，百分比型行情字段为小数，控盘系数为百分数；前端会自动换算为亿元和百分比。

筛选先对全量候选执行轻量资格判断和排序，再为最多 500 条概念及 500 条个股生成逐日解释。截断不改变合格概念的成分股范围，响应仍提供完整合格数量和截断标志。两种筛选接口共用一个并发槽，忙时返回 `503 scan_busy` 与 `Retry-After`；ST 状态及股票名称以所选日的日终记录为准，不使用旧成员关系中的名称。

请求示例：

```bash
curl -X POST 'http://localhost:8080/api/v1/focus/scan' \
  -H 'Content-Type: application/json' \
  -d '{
    "as_of":"2026-08-14",
    "consecutive_days":3,
    "concept_match":"all",
    "concept_conditions":[{"field":"turnover","operator":"gt","value":50000000000}],
    "stock_match":"all",
    "stock_conditions":[{"field":"turnover","operator":"gt","value":200000000}],
    "stock_scope":{"main_board_only":true,"exclude_st":true,"require_qualified_concepts":true}
  }'
```

可用操作符为 `gt`、`gte`、`lt`、`lte`、`eq`、`between`；可用字段见 `backend/openapi.yaml`。兼容接口 `GET /api/v1/focus/three-day?as_of=YYYY-MM-DD` 执行上述完整初始模板。

前端长列表采用固定分页：首页行业/概念榜和板块成分股每页 25 条，采集运行记录每页 20 条，收盘个股榜由后端分页且每页 100 条。单日日期仍使用日期控件；当天非交易日时默认回退到上一个交易日。

## 斐讯 N1 部署

建议系统为 64 位 Armbian，SQLite 数据目录必须放在 USB 外接 SSD。不要将高频数据库写入长期放在 N1 内置 eMMC。

```bash
echo "$GHCR_READ_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin
docker compose pull
docker compose up -d
```

仓库地址为 `github.com/roiding/shadowflow`。GHCR 私有包需要具有读取权限的 Personal Access Token；公开包可以省略登录。`compose.yaml` 使用发布镜像，没有 `build` 配置；日常部署应使用 Actions 产出的固定镜像标签或摘要。

GitHub Actions 位于 `.github/workflows/arm64-image.yaml`。它先运行 Go race 测试/`go vet`、脚本回归和 React lint/test/build，再构建 `linux/arm64` 镜像。Docker 构建阶段也运行后端测试/vet、前端 lint/test/build；只有 npm 漏洞检查及 `govulncheck` 对实际目标架构的 `shadowflow`、`collect` 两个二进制扫描都成功，才可生成最终镜像。发布前另以 Trivy 检查最终镜像的操作系统和库，HIGH/CRITICAL 告警（包括尚无补丁的告警）会阻止发布；再经 QEMU 验证就绪、鉴权、静态页面和 Alpine 备份/日历脚本。所有门槛通过后，`main` 分支推送、`v*` 标签或手工触发才发布 GHCR，Pull Request 不发布。

容器基线固定为 `golang:1.26.8-alpine3.24`、`node:22.23.2-alpine3.24` 和 `alpine:3.24.1`；`golang.org/x/text` 更新为 v0.39.0。Go 构建设置 `GOTOOLCHAIN=local`，避免自动改用未核验版本；`go.mod` 的 1.25.0 仅是语言最低要求，不是生产安全补丁要求。本机 Go/Node 无需为部署改装。运行时不包含 Go/Node 工具链，只含服务、采集器、静态前端和 SQLite 等系统包；Compose 内存限制为 768 MiB。

CI 每轮设置不同的 `VULN_DB_REFRESH`，重新更新运行时 Alpine 包并执行漏洞扫描，不复用旧安全结果。手动源码构建也应刷新该参数：

```bash
docker buildx build --platform linux/arm64 --load \
  --build-arg VULN_DB_REFRESH="$(date -u +%Y%m%dT%H%M%SZ)" \
  -t shadowflow:local .
```

构建和扫描需要外网访问官方依赖及漏洞库；失败不会降级为放行。手动构建仍需执行最终镜像扫描和容器烟雾检查，不能以本地源码扫描代替。工具链标签需随安全更新维护，并通过相同门槛后再部署。

关键环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `SHADOWFLOW_DATABASE_PATH` | `/data/shadowflow.db` | SQLite 文件路径，固定配置 |
| `SHADOWFLOW_CALENDAR_PATH` | `/app/config/trading_calendar.json` | 本地交易日历 |
| `SHADOWFLOW_CALENDAR_AUTO_UPDATE` | `true` | 覆盖期不足时是否从交易所年度休市安排自动刷新 |
| `SHADOWFLOW_CALENDAR_SOURCE_URL` | 上交所年度休市安排页 | 自动更新来源 |
| `SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS` | `45` | 距离显式日历到期多少天时开始刷新 |
| `SHADOWFLOW_STATIC_DIR` | `/app/web` | React 构建产物 |
| `SHADOWFLOW_PAGE_SIZE` | `100` | 上游分页大小 |
| `SHADOWFLOW_REQUEST_TIMEOUT_SECONDS` | `5` | 单次上游请求超时 |
| `SHADOWFLOW_QUOTE_BASE_URLS` | `https://push2.eastmoney.com,https://push2delay.eastmoney.com` | 行情接口候选域名；主域空响应、网络错误或临时服务错误时切换 delay 域 |
| `SHADOWFLOW_SCHEDULER_ENABLED` | `true` | 是否运行盘中和盘后采集调度；健康检查或只读 API 模式可设为 `false` |
| `SHADOWFLOW_SUCCESS_RUN_RETENTION_DAYS` | `30` | 成功/跳过的采集运行记录保留天数 |
| `SHADOWFLOW_FAILURE_RUN_RETENTION_DAYS` | `180` | 失败/部分成功的采集运行记录保留天数，必须不少于成功记录保留天数 |
| `SHADOWFLOW_BACKUP_RETENTION_DAYS` | `3` | 有效压缩备份保留数量，必须为正整数，不是自然日；只清理自动命名且校验通过的备份及其 sidecar |
| `SHADOWFLOW_API_TOKEN` | 空 | 非空时开启 `/api/v1/*` 和 `/metrics` 的 Bearer Token 鉴权，至少 16 个字符；为空时关闭 |

其余运行参数已固定在 `compose.yaml` 中，通常不需要额外配置。

`backend/config/trading_calendar.json` 已内置 2026 年 A 股休市日期和 `valid_through`。服务每天检查覆盖期，距离到期不足阈值时读取交易所年度休市安排；只有年度标题、日期范围和最少假日数全部校验通过才原子替换，失败时保留旧文件。覆盖状态同时出现在 `/api/v1/system/status` 和 Prometheus 指标中。

## 备份和恢复

`backup.sh` 是手动备份脚本，使用 SQLite `.backup`，不会直接复制 WAL 模式下可能不完整的主文件：

```bash
docker exec shadowflow /app/scripts/backup.sh
```

备份先在备份目录的独立暂存目录中执行 SQLite `integrity_check` 和 gzip 校验，再通过同文件系统的硬链接占用最终名称，不覆盖并发备份；同秒重名会等待下一秒重试。最后发布 `.db.gz.sha256`，作为完成标记，配套 `.meta` 保存关键表计数。该目录所在文件系统需支持硬链接。

`SHADOWFLOW_BACKUP_RETENTION_DAYS` 默认保留最近 3 个有效备份：只有自动命名、同时具备 `.meta` 和 `.sha256`、摘要及 gzip 校验通过的归档才计数、参与清理。半成品、损坏文件和手工命名文件不挤占名额，也不自动删除；异常退出遗留的隐藏暂存目录需在确认无备份进程后人工清理。备份目录需容纳未压缩快照、压缩暂存文件及保留副本。`backup.sh` 仍可在任何时间手动执行。

线上自动任务应调用先判断交易日的包装脚本：

```bash
docker exec shadowflow /app/scripts/auto-backup.sh
```

它通过 `/app/collect -task is-trading-day -date YYYY-MM-DD` 读取 `SHADOWFLOW_CALENDAR_PATH`，复用服务的 JSON 日历解析与日期校验，不打开数据库、不读取上游。周末或休市日跳过，显式 `workdays` 覆盖周末；文件缺失、JSON/日期非法或节假日与工作日冲突时失败，不调用备份。主机 cron 仍可每天触发入口。非容器环境可设置 `SHADOWFLOW_COLLECT_BIN` 和 `SHADOWFLOW_BACKUP_SCRIPT` 指向本机程序。

恢复会对传入归档的私有副本计算 SHA-256，再解压同一副本；不使用 sidecar 中的文件路径寻找被校验文件。新 sidecar 只记录文件名，旧版绝对路径 sidecar 和迁移后的归档也兼容。恢复验证需要数据库目录中的临时空间，可先做不落库验证：

```bash
docker compose run --rm --entrypoint /app/scripts/restore.sh shadowflow \
  /backups/shadowflow-YYYYMMDD-HHMMSS.db.gz --dry-run
```

恢复时先停止服务，再运行：

```bash
docker compose stop shadowflow
docker compose run --rm --entrypoint /app/scripts/restore.sh shadowflow /backups/shadowflow-YYYYMMDD-HHMMSS.db.gz
docker compose start shadowflow
```

## 手工补采

服务停机或错过盘后任务后，可以使用管理命令补采。补采板块时必须提供真实采样时间，不应把历史收盘数据伪装成盘中分钟序列：

```bash
cd backend
go run ./cmd/collect -task boards -date 2026-08-13 -at 14:30
go run ./cmd/collect -task end-of-day -date 2026-08-13
go run ./cmd/collect -task stock-kline -date 2026-08-13
go run ./cmd/collect -task cleanup -date 2026-08-14
go run ./cmd/collect -task maintenance -date 2026-08-14
go run ./cmd/collect -task analytics -date 2026-08-14
go run ./cmd/collect -task relations -date 2026-08-13
```

镜像部署可直接使用内置的 `/app/collect`，它会沿用 Compose 中的数据库挂载和环境变量：

```bash
docker compose run --rm --entrypoint /app/collect shadowflow -task relations -date 2026-08-13
```

`end-of-day` 在 `16:00` 执行，失败时于 `16:05`、`16:10` 补试，原子写入行业、概念、个股各自的完整日终榜和 48 个盘后修订资金点。`stock-kline` 在 `16:15` 执行，并于 `17:30`、`20:00` 补试，保存有成交个股的 48 根未复权五分钟 K。五分钟接口不可用时，会从同源 `trends2` 取得当日 241 根一分钟 OHLC，按 `09:35-11:30`、`13:05-15:00` 聚合为 48 根并与日 K 的 OHLC 交叉校验；分钟量额保留原值，不与可能包含 `15:00-15:30` 盘后交易的日终截面强制一致，241 根源数据也不长期保存。K 线任务以当日已归档日终个股截面为候选源，每只股票只有完整 48 根才会在事务中落库；整批允许部分成功，后续运行只补当日缺失股票。历史日没有当日主归档时不依赖事后猜测，系统以每日实际采集为准。`cleanup` 每天 `09:00` 检查上一交易日：两类板块资金与日终、个股资金、五分钟 K、完整日终榜和日 K 任一不完整，都不会删除盘中工作数据。

调度实例持久化在 SQLite `scheduled_job` 表中，使用原子 claim 和 lease 防止进程重启后重复执行；失败任务按类型保留重试预算，过期 lease 会自动回到队列。启动时会回补当日已错过且可安全重放的非分钟级任务。盘中采集、关系同步和盘后归档使用独立调度通道，关系扫描变慢不会阻塞分钟采集。每天 `09:05` 维护任务会按保留策略清理运行日志、调度任务、回收旧的临时原始响应、执行被动 WAL checkpoint，并按约 30 天周期运行 `PRAGMA optimize`。`/api/v1/research/quality` 的 `meta.archive_manifest` 提供统一的每日归档清单、代码集合摘要、来源契约和校验错误。

## 归档完整性和分析数据

每个交易日只保留一份日终三榜、板块 48 点、个股资金/K 线、K 线来源和日终原始响应。`revision_id` 仅作为兼容现有接口的轻量归档标识和内容摘要，不再复制主数据；同日重跑更新原归档及其摘要。常用接口：

```bash
curl 'http://localhost:8080/api/v1/research/revisions?trade_date=2026-08-14'
curl 'http://localhost:8080/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-14&revision_id=<revision_id>'
curl 'http://localhost:8080/api/v1/research/features?trade_date=2026-08-14&type=stock'
curl 'http://localhost:8080/api/v1/research/labels?trade_date=2026-08-14&type=stock&horizon=5'
```

日级特征包括有符号暗盘活跃度、资金强度、控盘系数、横截面百分位、`5/10/20/60` 日自身百分位、排名变化、连续流入、资金加速度，以及 48 点曲线的早/午/尾盘占比、最大流入流出时段、尾盘加速度、回撤、反转和价资背离。滚动窗口未积满时对应字段保持空值，不用短样本伪装完整窗口。

未来标签严格按后续完整交易日生成，包含收益率、相对首要行业收益、最大有利波动和最大不利波动。目标交易日重跑时会根据更新后的主归档重新计算。

K 线已经落库但封存失败时，常规重试和启动恢复仍会执行封存；内容摘要未变化且特征已存在时不会重复生成分析。补入中间交易日会替换受影响周期的旧目标映射；修正某日归档会在同一分析事务内重算该日及后续最多 59 个完整归档日的滚动特征和相应标签。

旧版本已经产生的分析错误不会在本机审计过程中修改生产数据。部署修复后，可对最早受影响的日期执行 `collect -task analytics -date YYYY-MM-DD` 重新计算；若影响范围超过 60 个归档日，需分段覆盖。金额/行情主数据缺损需先重采，再重新封存。

采集运行记录使用任务 context 的截止时间加一分钟收尾余量作为租约。打开管理进程不会把其他活跃任务判为中断；初始化和维护只回收租约明确过期的任务。没有租约的历史运行记录不会被推断为已停止，需保留并核对后处理。

动态筛选结果包含逐日逐条件实际值和通过状态；未入选解释返回首个失败日或范围剔除原因。前端支持本地模板保存、删除、JSON 导入和复制分享。多概念成分股使用一次批量截面查询，不再逐概念访问数据库。

## 研究工作站导出

CSV 导出仅依赖 Python 标准库；Parquet 使用 Zstd 压缩并需要 `pyarrow`：

```bash
python3 -m pip install pyarrow
python3 scripts/export_research.py \
  --database data/shadowflow.db \
  --output exports/2026-08 \
  --from-date 2026-08-10 \
  --to-date 2026-08-17 \
  --format both
```

导出目录包含日期范围内的归档摘要、`daily_close`、`daily_features`、`future_labels`、板块资金曲线和个股五分钟联合数据。导出先通过 SQLite online backup 固定一份磁盘快照，之后关闭源库连接，CSV、Parquet 和 `manifest.json` 均从同一快照生成；系统临时目录需额外容纳一份未压缩数据库，不会为整个导出过程持续持有源库读事务。

仅导出完整清单与封存版本匹配、滚动特征来源仍有效的当前归档日。未封存、重采中、清单已变化但尚未重新封存、依赖窗口过期的日期不会混入，清单的 `excluded_dates` 列出范围内已存在但被排除的日期；范围内没有可导出的日期时直接失败。无内容变化的重复封存也会同步清单元数据。旧库被排除的日期需先完成采集/封存或修复分析，再重新导出。

日期范围对收益标签按信号日应用，目标日可以在范围之外，但信号和目标都必须是有效当前版本，`label_target_revisions` 记录实际目标的摘要。为兼容旧脚本，保留 `future_label_history` 文件名；此文件现在与 `future_labels` 同为有效当前标签，不再混入过期目标历史。`manifest.json` 同时记录归档 SHA-256、固定快照方式及各文件行数。

`--output` 必须为新目录或空目录。所有文件成功后才将同一父目录下的暂存包原子发布，失败不留下可误读的半套导出，也不覆盖已有导出；父目录需额外容纳整个暂存包。

关系维护按东方财富行业目录 `t:2` 和广义概念目录 `t:3` 逐板块反查全部成分股。它会在交易日开盘前的 `07:00` 自动执行，失败或当天尚未成功时在 `07:30`、`08:00` 补试。自动关系任务最晚运行至当日 `09:15`；到点取消未完成扫描，排队或重启后迟到的任务标记为跳过，也不再安排越过截止时间的重试，盘中继续使用上次完整关系。目录每次从上游重新获取，按板块代码稳定分页，新增行业和概念自动纳入；分页途中总数变化、重复代码或缺页会让本轮失败重取。扫描数据逐板块写入临时表，不在内存中保存全市场关系；只有完整扫描成功后，才会在一个事务中写入首次全量基线或当日 `added`/`removed` 事件并更新物化当前态。新增行业/概念、个股新增或删除概念、个股行业变更都统一表现为关系事件，任意历史日期的成分由基线加截至当日的事件重建，不保存每日目录快照。中途失败只清理临时数据，不会改变已有关系。首次部署或错过调度时可手工执行 `-task relations`。

## 验证

导出、备份及交易日包装脚本的回归使用临时数据库，不接触业务库。需要 Python 3.9+、Go、`sqlite3`、`gzip`、`sha256sum`；有 `dash` 时另跑一套 shell 回归，有 `pyarrow` 时额外验证 CSV/Parquet 逐值一致，CI 会安装并执行这两项：

```bash
python3 -m unittest discover -s scripts/tests -v
```

```bash
cd backend && go test ./... && go vet ./...
cd ../frontend && npm run lint && npm test && npm run build
curl http://127.0.0.1:8080/health/ready
curl -H "Authorization: Bearer $SHADOWFLOW_API_TOKEN" http://127.0.0.1:8080/metrics
```

完整业务口径和实施阶段见 [项目规划.md](项目规划.md)。
