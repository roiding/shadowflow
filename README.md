# ShadowFlow 暗流

面向行业、概念资金趋势研究的单机 Web 系统。Go 服务在交易日 `09:31-11:30`、`13:01-15:00` 每分钟采集完整板块榜；盘后将 `09:35-11:30`、`13:05-14:55` 的 47 个五分钟点沉淀为研究数据，将行业、概念 `15:00` 另存为日终快照，并在 `15:10` 抓取完整个股收盘榜。三类榜单统一归入当天 `daily_close` 数据集，可联合查询和导出。盘后还会维护个股所属行业、概念：首次建立全量基线，后续交易日仅永久记录新增和删除事件，并可还原任意截面日的归属。React 前端默认每 60 秒读取本地 Go API。

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
curl 'http://localhost:8080/api/v1/boards/concept/BK1128/stocks?as_of=2026-08-13'
curl 'http://localhost:8080/api/v1/relations/changes?trade_date=2026-08-13'
```

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
| `SHADOWFLOW_STATIC_DIR` | `/app/web` | React 构建产物 |
| `SHADOWFLOW_PAGE_SIZE` | `100` | 上游分页大小 |
| `SHADOWFLOW_REQUEST_TIMEOUT_SECONDS` | `5` | 单次上游请求超时 |
| `SHADOWFLOW_QUOTE_BASE_URLS` | `https://push2.eastmoney.com,https://push2delay.eastmoney.com` | 行情接口候选域名；主域空响应、网络错误或临时服务错误时切换 delay 域 |
| `SHADOWFLOW_SCHEDULER_ENABLED` | `true` | 是否运行盘中和盘后采集调度；健康检查或只读 API 模式可设为 `false` |

`backend/config/trading_calendar.json` 已内置 2026 年 A 股法定休市日期，部署跨年或交易所临时调整前必须更新。未列入 `holidays` 或 `workdays` 的日期会按周一至周五判断；服务启动时会拒绝非法日期或同时列入两组的冲突配置。

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
go run ./cmd/collect -task compact -date 2026-08-13
go run ./cmd/collect -task daily-close -date 2026-08-13
go run ./cmd/collect -task relations -date 2026-08-13
```

`compact` 会在同一个 SQLite 事务中写入 47 个研究点、行业/概念 15:00 日终快照、质量摘要并清理分钟工作表。调度器在 `15:05` 执行，失败时于 `15:07`、`15:09` 补试。若缺失任一板块的 15:00 点，任务明确失败且保留分钟工作表供重试，不会静默补填。单独使用 `-task cleanup` 前应先确认研究和日终沉淀均成功。

关系维护按东方财富行业目录 `t:2` 和广义概念目录 `t:3` 逐板块反查全部成分股。它会在交易日开盘前的 `08:00` 自动执行，失败或当天尚未成功时在 `08:50`、`09:15` 补试，因此盘中分析可读取当天的最新归属关系。扫描数据逐板块写入临时表，不在内存中保存全市场关系；只有完整扫描成功后，才会在一个事务中写入基线或当日 `added`/`removed` 事件并更新物化当前态。中途失败只清理临时数据，不会改变已有关系。首次部署或错过调度时可手工执行 `-task relations`。

## 验证

```bash
cd backend && go test ./...
cd ../frontend && npm run build
curl http://localhost:8080/health/ready
curl http://localhost:8080/metrics
```

完整业务口径和实施阶段见 [项目规划.md](项目规划.md)。
