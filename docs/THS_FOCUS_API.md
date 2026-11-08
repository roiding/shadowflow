# 同花顺涨停聚焦：接口、会话与项目接入

核查日期：2026-11-07，上海时区。样本为用户提供的 `Stream-2026-11-07 09:59:13.har`，共 138 次请求、47 组方法与路径。仅提取市场研究所需请求；原始 HAR 含账号会话，不提交到仓库。

## 匿名访问的实测结果

探测请求不带 Cookie、Authorization、hexin-v、x-fuyao-auth，使用正常 TLS 验证。下表中的可用性只代表本次请求日期、参数和时间点；HTTP 200 之外还核验业务状态及实际数据。

| 接口族 | 数据与用途 | 本次登录依赖与边界 |
| --- | --- | --- |
| `dataapi/limit_up/{limit_up_pool,open_limit_pool,lower_limit_pool}` | 完整涨停、炸板、跌停事件 | 匿名成功；按分页校验；仍是联动图的主数据源 |
| `continuous_limit_pool` | 连板候选、几天几板、封单金额 | 匿名成功；源“几天几板”与连续封板天数不混算 |
| `continuous_limit_up` | 按高度分层的股票梯队 | 匿名成功；无顶层日期回显，保留请求日期与限制 |
| `block_top` | Top题材、相关涨停股票、源涨停原因和详细叙事 | 匿名成功；只返回Top20，不能当完整行业/概念成份；源叙事含AI生成内容 |
| `limit_up`、`change?type=latest` | 冲刺涨停、最近异动 | 匿名成功；不能据最新一条异动还原全天秒级事件 |
| `mobileapi/hotspot_focus/stock_pool/v1/get_limit_up_stocks` | 首板、各级连板、封单和源行业信息 | 匿名成功；历史2025-11-04、2026-06-04亦返回数据；未统一不同接口的股票范围 |
| `get_limit_up_fail_stocks` | 按晋级级别分类的失败股票池 | 匿名成功；与当日炸板池不是同一集合，不能直接替换炸板事件 |
| `get_hundred_high_stocks`、`get_trend_stocks`、`get_drawdown_stocks` | 百日新高、趋势、打板大面及趋势大面 | 匿名成功；部分HAR只抓了第一页，实际需要全量分页；选择条件以源口径记录 |
| `limit_up_compare/v1/two_days_compare` | 昨日候选、今日晋级、首板与最高板 | 匿名成功；回显前后日期；`rate`为涨跌幅，不是胜率 |
| `limit_up_compare/v1/range_compare` | 最近30个交易日的连板对照 | 匿名成功；最新日的`seal_nextday=false`可能尚未结算，不能直接当失败 |
| `chart/v1/get_chart` | 连板数量、晋级率、情绪、最高板、新高/趋势/大面数量等历史序列 | 匿名成功；`size`是点数，363/365点跨越一年以上；下调`end_time`成功取得2023年开始的较早窗口，未确定所有指标的精确最早日期 |
| `market_state/v1/overview`、`get_wind_vane_stock` | 市场涨跌家数、量能、高位/龙头/异动集合 | 匿名成功；市场默认范围与三池scope不完全相同；例如33只与32只涨停并存 |
| `ozone.../rank/{tab_list,all_tab_data,summary_image,list_recent_date}` | 涨停简图、题材分类、异动原因和详细原因 | `all_tab_data`对2026-11-06、11-05、10-23匿名成功且日期匹配；图片为PNG；最近日期列表为10日，但不等于数据只保留10日 |
| `dataq.../fetch/v1/specific_data` | 指定股票及指定指标的批量查询 | 匿名401、`cookie is empty`；仅风控Cookie仍401；只复用账号Cookie的探测虽然HTTP200，但核心指标为`NO_AUTH`和空值；原始App HAR中这些指标有实际数值，不能把两者视为同一验收结果 |
| `hotspot_focus/permission/v1/query`、`chart_config/v1/query_config` | 账号权限和个人图表配置 | 匿名失败，账号会话成功；配置不可用不影响已知公开图表数据接口 |
| `ozone.../permission/query?cate=review_history` | 名为`review_history`的权限位 | 匿名`no permission`，当前账号返回`is_valid=false`；数据接口仍可查多个历史日期，具体权限控制范围未证实 |
| `quota-h.../single_kline` | 抓包中的上证指数日K | 去掉Cookie和签名头仍成功；只验证了该指数和日K参数，不能据此声称个股、五分钟或长期回补均可用 |

`review_history`是抓包中真实出现的权限查询参数，不是已确认的App页面名称，也不是本项目归档三池数据的前提。当前账号对这个权限位返回`is_valid=false`，同时多日历史简图与三池仍可读取。因此不能由这个名字推断用户缺少“历史复盘”功能或必须购买权限；目前只有权限查询本身的响应得到验证。

广告、埋点、性能报告、论坛推荐、关注关系不进入研究归档。源配置名不等于数据能力：部分“昨日涨停今日收益”等图表只有配置记录，尚未验证对应字段与结算口径。

图表数据另有可见口径异常：高板晋级字段可超过100，部分情绪分位数的字段名与大小关系不吻合。接入时保留源值，不能直接当概率或经过本地验证的因子。

## 本轮项目接入

主事件仍由三池决定。新增独立 `ths_focus_day` 保存题材、梯队、市场概览与两日晋级对照、抓取时间、原始响应和覆盖限制。三个事件池和补充数据分别发布，补充源失败不删除已经完整的事件归档。

- 原 `end-of-day-events` 盘后任务会分别检查事件归档和补充归档，重试时跳过已完成分项。Compose中的事件开关按部署选择默认开启；直接运行Go程序时默认关闭，实际运行以配置为准。
- 手动补充入口：`collect -task ths-focus -date YYYY-MM-DD -scope HS,GEM2STAR`。不会自动回补全年。
- `GET /api/v1/limit-up/focus?trade_date=YYYY-MM-DD&scope=HS,GEM2STAR` 只读本地。没有数据返回`not_collected`，不在打开页面时采集上游。
- 股票联动页提供辅助题材、来源解释和梯队信息；连线继续使用同日事件共现，不因题材相同自动加边。
- `block_top`和梯队按请求scope记录，概览和两日对照使用`source_default`口径。部分接口没有日期回显，`source_date_verified=false`，界面显示限制；不能作为严格点时回测事实。
- 跌停请求加入`330333/330334`，存储真实`first_limit_down_time/last_limit_down_time`，股性新增首次跌停时刻中位数和有效样本数。老记录缺失时保持未知，须重采后才有时间数据。

其余股票池、情绪历史图表和完整题材简图已完成接口评估，尚未全部接成页面与生产归档。尤其不能把筛选后的盘中预览当成当时的全市场股票池，也不能把事后更新的原因当成事前可知信息。

账号指标批次必须通过日期、股票身份、指标映射和权限校验。`attribute.permission_flag=NO_AUTH`属于指标权限拒绝，不计为成功归档，也不通过重试密码登录解决；正常缺值仍保留为`null`。原始App请求有非空指标，而此前Cookie复用探测未完整复现请求上下文，因此不能由`NO_AUTH`推断账号必须购买权限。原HAR中9只可交叉比对股票的`up_down_stock_limit_up_volume_money_new`与公开池`order_amount`一致，口径为封单额。

账号记录保留指标名称、日期类型、时间戳及权限元数据，按当日所需股票身份检查完整性并补采缺项。旧版缺失指标映射的记录不作为完成依据；补采中断后已有完整记录可复用。当前账号数据用于独立归档，尚未进入联动图或股性画像；这些页面的题材与梯队仍来自公开补充接口。

## 官方登录流程

已直接读取官方登录页及其公开脚本，确认：

1. 登录页：`https://upass.10jqka.com.cn/login`。
2. 公钥：`GET /pubkey/default.js`，提供RSA版本、模数和指数。
3. 盐值：`POST /user/getGS`，提交加密用户名、公钥版本与随机数。
4. 密码登录：`POST /login/dologinreturnjson2`。用户名和密码摘要经RSA加密，`passwdSalt`遵循官方SHA256/HMAC及盐值处理。服务端可能要求第二步盐值交换。
5. `errorcode=0`后还检查私有图表配置接口，确认新Cookie能读取账号数据，再作为有效会话保存。

官方密码页面明确拒绝手机号、邮箱作为用户名；这两类登录需要走短信或其他交互流程。已验证的滑块挑战支持有界协议处理；验证码未通过、设备处置、账号拒绝、弱密码修改要求均进入需要人工处理状态，不无限重试。密码本身不会写入日志或接口响应。

### 自动登录与请求重试

账号接口统一经过会话管理器。HTTP401、指标查询的业务401，以及已验证的私有图表配置`status_code=-2`触发过期处理；权限查询只将实测的“需要授权”响应识别为登录失效，普通“无权限”、403、429、网络错误不触发密码登录。一个刷新任务为并发请求共享；已经完成刷新后才到达的旧401复用新会话。登录成功后原请求最多重放一次，再次失效即返回错误。数据查询POST保留原始URL、请求体与业务参数，重试不更换股票或交易日。

刷新失败默认冷却5分钟；错误密码、未解决的验证码与设备确认要求停止自动密码重试，等待显式`ths-login`或更新会话。一个等待者取消不会中断其他等待者的刷新，关闭管理器会取消其网络请求与刷新任务。会话只发往固定的同花顺数据域，重定向不会携带凭据跟随到其他站点。登录态默认写入应用数据库`ths_session`表（数据库文件收紧为0600），也可用`SHADOWFLOW_THS_SESSION_FILE`改回私有文件模式；两种模式都保留Cookie的域、路径、Secure及到期信息，响应中的Cookie轮换和删除同样持久化，Max-Age转换为绝对到期时间，重启不会延长其寿命。2026-11-08已由本机连接真实同花顺接口验证完整链路：无头浏览器握手（10个握手Cookie）+密码协议+滑块伪造+重提交全部成功，凭据正确时服务端返回`errorcode=0`；这不替代Linux容器上的独立验收。

常驻管理器在账号请求前及认证失效后检查共享会话内容。另一个进程完成`ths-login`后，下一次请求采用新会话并清除旧的人工处理状态，无需重启；相同的旧会话或已过期Cookie不会解除密码重试停止状态。旧请求准备持久化Cookie轮换时也会检查共享会话，发现新会话便放弃旧响应回写。选择`SHADOWFLOW_THS_SESSION_FILE`后，CLI与服务均使用该文件，文件缺失不会悄悄回退到数据库；文件模式的登录与状态命令不要求打开应用数据库。

浏览器握手要求成功导出非空且稳定的同花顺Cookie集合，使用该集合中最新的`v`。CDP错误、导出失败、空集合、取消和超时均返回握手失败，不能只因先前页面出现过`v`就继续密码提交。登录成功验收仍须与账号指标有效性、Linux容器运行验收分别记录。

配置项：

| 环境变量 | 默认 | 用途 |
| --- | --- | --- |
| `SHADOWFLOW_THS_AUTO_LOGIN` | `true` | 账号数据请求失效后自动登录；要求凭据与无头浏览器配置齐全 |
| `SHADOWFLOW_THS_USERNAME` / `SHADOWFLOW_THS_PASSWORD` | 空 | compose 环境变量凭据（`docker inspect` 可见，部署方接受此取舍）；与凭据文件二选一 |
| `SHADOWFLOW_THS_CREDENTIALS_FILE` | 空 | 私有JSON文件，字段`username/password`（开发模式） |
| `SHADOWFLOW_THS_SESSION_FILE` | 空 | 私有持久会话文件；设置后优先使用文件，不设置时入库 |
| `SHADOWFLOW_THS_HEADLESS_BROWSER` | `/usr/bin/chromium-browser` | 无头浏览器路径；容器镜像已内置 Chromium 与 CJK 字体 |
| `SHADOWFLOW_THS_CHROME_ARGS` | 容器内 `--no-sandbox --disable-dev-shm-usage` | 浏览器附加启动参数 |

本地开发或临时录入凭据（文件模式）：

```sh
python3 scripts/ths_credentials.py /absolute/private/ths/credentials.json
cd backend
SHADOWFLOW_THS_AUTO_LOGIN=true SHADOWFLOW_THS_CREDENTIALS_FILE=/absolute/private/ths/credentials.json SHADOWFLOW_THS_HEADLESS_BROWSER=/Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome go run ./cmd/collect -task ths-login          # 登录态写入数据库
SHADOWFLOW_THS_AUTO_LOGIN=true go run ./cmd/collect -task ths-auth-status
SHADOWFLOW_THS_AUTO_LOGIN=true go run ./cmd/collect -task ths-account-data -date 2026-11-06 -codes 33:002578,17:603381
```

线上部署不再需要挂载凭据或会话文件：凭据由 compose 环境变量提供，登录态保存在 `/data/shadowflow.db` 中并跨进程共享。每日盘后调度：`end-of-day-events`（涨停事件+同花顺题材补充）在 `16:30/16:35/16:40`，受限账号数据 `ths-account-data` 在 `16:50/17:10/17:50` 自动按当日涨停名单分批（每批≤20只）采集入库。不要把真实密码放进 Git 或聊天记录；compose 环境变量的可见范围由部署方自行评估。

账号调度只在服务实际接入账号数据源后启用，并继续尊重事件开关、`ths-account-data`及父级`end-of-day`的禁用配置。未配置凭据/浏览器或未开启自动登录时，不排队也不领取旧账号任务，公开事件归档仍可独立运行。上表的自动登录默认值指Compose配置；直接运行Go程序时默认`false`。

## 本次验收与运行状态

- 2026-11-07在真实匿名源上重新抓取2026-06-04扩展范围，校验后以90条涨停、24条炸板、32条跌停、114只触涨停发布。源23/113、证据代码`002856`及完整原始页保留在审计记录中。
- 该缺日已经手动补入N1；默认与扩展范围均覆盖2025-10-22至2026-11-06的254个交易日。原有507个范围日的内容哈希均未变化，仅新增目标日146条事件。
- 2026-11-06的四类补充接口在本地实际采集并通过验证，得到20个题材、4个梯队及4份原始页；没有据此自动回补全年补充数据。
- 自动登录通过本地HTTP/TLS、并发、过期、冷却、交互挑战、精确POST重放、Cookie持久化与轮换测试。受限指标入口已接会话管理器；公开归档不需要启动账号管理器。
- 生产旧版可读取已补事件，完整计数审计提示与新补充页面需发布本轮代码后生效；部署与Linux容器账号验收应单独记录，不能由本地测试推断。
