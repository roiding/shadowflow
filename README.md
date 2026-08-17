# ShadowFlow 暗流

面向行业、概念和个股资金趋势研究的单机 Web 系统。Go 服务在交易日 `09:31-11:30`、`13:01-15:00` 每分钟采集完整板块榜，仅供盘中实时展示。每天 `16:00` 从东方财富 `darktrade` 与 `darktradetick` 独立重新抓取行业、概念、个股完整日终榜及 48 个五分钟累计资金点；`16:15` 起抓取有成交个股的 48 根未复权五分钟 K。完整日归档会封存为不可变 revision，重跑只前移当前指针，不覆盖旧版本；每个 revision 自动生成日级特征，后续交易日到来后补齐 `1/3/5/10/20` 日收益标签。盘中工作数据只在长期数据全部完整后由次日 `09:00` 任务清理。

东方财富数据源资料见 [项目记忆](项目记忆.md)。`push2/push2his` 的请求参数、Cookie 状态窗口、重试策略和故障判定统一以 [行情接口调用规范](docs/EASTMONEY_PUSH2_API.md) 为准。

## 本地开发

需要 Go 1.25、Node.js 22 和 npm。

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

访问 [http://localhost:8080](http://localhost:8080)。API 规范见 [backend/openapi.yaml](backend/openapi.yaml)，Prometheus 指标位于 `/metrics`。

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

## 动态连续筛选

前端“动态筛选”页允许分别为概念和个股添加或删除任意条件，选择“全部满足”或“任一满足”，并配置连续交易日数、是否仅主板、是否排除 ST、个股是否必须属于命中概念。内置初始模板就是原始策略：连续 3 日，概念成交额大于 500 亿元、个股成交额大于 2 亿元，换手率大于 3%，涨幅 1%–6%，控盘系数 1.5%–6%。控盘系数按 `(主力明盘 + 主力暗盘) / 成交额 × 100%` 即时计算。

筛选读取本系统已经原子归档的完整 `daily_close`，不请求东方财富概念历史 K 线。只有累积日期达到所选连续日数才会计算结果；不足时接口明确返回 `ready=false`，不会补值或推测。停牌个股不会使整日失效，但它本身因缺少可用行情而不会入选。

通用接口为 `POST /api/v1/focus/scan`。API 使用原始单位：资金/成交额为元，百分比型行情字段为小数，控盘系数为百分数；前端会自动换算为亿元和百分比。示例：

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
mkdir -p /mnt/ssd/shadowflow/data /mnt/ssd/shadowflow/backups
export SHADOWFLOW_DATA_DIR=/mnt/ssd/shadowflow/data
export SHADOWFLOW_BACKUP_DIR=/mnt/ssd/shadowflow/backups
export SHADOWFLOW_IMAGE=ghcr.io/roiding/shadowflow:v0.1.0
docker compose pull
docker compose up -d
```

仓库地址为 `github.com/roiding/shadowflow`。GHCR 私有包需要具有读取权限的 Personal Access Token；公开包可以省略登录。也可以使用 `docker compose up -d --build` 从源码构建，但日常部署应优先使用 Actions 产出的固定镜像标签或摘要。

GitHub Actions 位于 `.github/workflows/arm64-image.yaml`。它先运行 Go 测试/`go vet` 和 React lint/build，再构建 `linux/arm64` 镜像，通过 QEMU 实际启动容器并检查架构、数据库就绪状态和 React 首页；只有全部通过后，`main` 分支推送、`v*` 版本标签或手工触发才会发布镜像到 GHCR，Pull Request 不发布。镜像是多阶段构建，运行时只包含单个 Go 服务、静态前端、SQLite CLI 和时区数据。默认内存上限为 256 MB，可按 N1 其他服务占用调整。

关键环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `SHADOWFLOW_DATABASE_PATH` | `/data/shadowflow.db` | SQLite 文件路径 |
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

`backend/config/trading_calendar.json` 已内置 2026 年 A 股休市日期和 `valid_through`。服务每天检查覆盖期，距离到期不足阈值时读取交易所年度休市安排；只有年度标题、日期范围和最少假日数全部校验通过才原子替换，失败时保留旧文件。覆盖状态同时出现在 `/api/v1/system/status` 和 Prometheus 指标中。

## 备份和恢复

在线备份使用 SQLite `.backup`，不会直接复制 WAL 模式下可能不完整的主文件：

```bash
docker exec shadowflow /app/scripts/backup.sh
```

默认保留 30 天，可通过 `SHADOWFLOW_BACKUP_RETENTION_DAYS` 调整。恢复时先停止服务，再运行：

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

`end-of-day` 在 `16:00` 执行，失败时于 `16:05`、`16:10` 补试，原子写入行业、概念、个股各自的完整日终榜和 48 个盘后修订资金点。`stock-kline` 在 `16:15` 执行，并于 `17:30`、`20:00` 补试，保存有成交个股的 48 根未复权五分钟 K。五分钟接口不可用时，会从同源 `trends2` 取得当日 241 根一分钟 OHLC，按 `09:35-11:30`、`13:05-15:00` 聚合为 48 根并与日 K 的 OHLC 交叉校验；分钟量额保留原值，不与可能包含 `15:00-15:30` 盘后交易的日终截面强制一致，241 根源数据也不长期保存。K 线任务以已归档日终个股截面为候选源，每只股票只有完整 48 根才会在事务中落库；整批允许部分成功，后续运行只补缺失股票，因此也可在之后补抓历史交易日。`cleanup` 每天 `09:00` 检查上一交易日：两类板块资金与日终、个股资金、五分钟 K、完整日终榜和日 K 任一不完整，都不会删除盘中工作数据。

服务启动时会在开盘前、收盘后或非交易日检查最近可安全补采的交易日；若日终归档或个股五分钟 K 不完整，会自动按顺序补采。盘中采集、关系同步和盘后归档使用独立调度通道，关系扫描变慢不会阻塞分钟采集。每天 `09:05` 维护任务会按保留策略清理运行日志、回收旧的临时原始响应、执行被动 WAL checkpoint，并按约 30 天周期运行 `PRAGMA optimize`。`/api/v1/research/quality` 的 `meta.archive_manifest` 提供统一的每日归档清单、代码集合摘要、来源契约和校验错误。

## 归档版本和分析数据

完整日归档首次完成或成功重跑后都会生成独立 `revision_id`，并保存日终三榜、板块 48 点、个股资金/K 线、K 线来源和日终原始响应。常用接口：

```bash
curl 'http://localhost:8080/api/v1/research/revisions?trade_date=2026-08-14'
curl 'http://localhost:8080/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-14&revision_id=<revision_id>'
curl 'http://localhost:8080/api/v1/research/features?trade_date=2026-08-14&type=stock'
curl 'http://localhost:8080/api/v1/research/labels?trade_date=2026-08-14&type=stock&horizon=5'
```

日级特征包括有符号暗盘活跃度、资金强度、控盘系数、横截面百分位、`5/10/20/60` 日自身百分位、排名变化、连续流入、资金加速度，以及 48 点曲线的早/午/尾盘占比、最大流入流出时段、尾盘加速度、回撤、反转和价资背离。滚动窗口未积满时对应字段保持空值，不用短样本伪装完整窗口。

未来标签严格按后续完整交易日生成，包含收益率、相对首要行业收益、最大有利波动和最大不利波动。目标交易日重跑会新增目标 revision 标签，旧标签不会覆盖。

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

导出目录包含日期范围内全部 `revisions`、当前 revision 对应的 `daily_close`、`daily_features`、`future_labels`、板块资金曲线、个股五分钟联合数据，以及保留全部目标版本的 `future_label_history`。`manifest.json` 记录每个交易日当前 revision 与内容 SHA-256，研究脚本可据此固定输入版本。

关系维护按东方财富行业目录 `t:2` 和广义概念目录 `t:3` 逐板块反查全部成分股。它会在交易日开盘前的 `08:00` 自动执行，失败或当天尚未成功时在 `08:50`、`09:15` 补试，因此盘中分析可读取当天的最新归属关系。扫描数据逐板块写入临时表，不在内存中保存全市场关系；只有完整扫描成功后，才会在一个事务中写入基线或当日 `added`/`removed` 事件并更新物化当前态。中途失败只清理临时数据，不会改变已有关系。首次部署或错过调度时可手工执行 `-task relations`。

## 验证

```bash
cd backend && go test ./...
cd ../frontend && npm run build
curl http://localhost:8080/health/ready
curl http://localhost:8080/metrics
```

完整业务口径和实施阶段见 [项目规划.md](项目规划.md)。
