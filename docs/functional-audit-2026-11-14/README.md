# 2026-11-14 功能审计证据

主报告在项目根目录 `功能审计报告-2026-11-14.md`；量化参考实现在相邻的 `quant-minimal-2026-11-14` 目录。

- `runtime-metadata.json`：镜像 revision、白名单运行配置、轻量归档清单和最近关系运行记录。
- `runtime-functional-evidence.json`：关系日状态及每日九次重试、事件/分类覆盖、真实 horizon 错位、只读关系 API 的元数据。
- `volume-contract-evidence.json`：按指定证券和指定股日查询的日量、分钟合计、换手和来源。没有扫描所有股票。
- `production-eastmoney-probe.json`：生产主机的两个串行小探针；没有触发全量采集或发布新关系版本。
- `*-history-probes.json`：各组请求参数、网络模式、时间、HTTP/业务状态及返回范围。默认网络路径与直连记录分开。
- `funds-window-probes.json`：日资金榜历史首屏和最新资金曲线的独立观察。
- `code-reproductions.log`：当前真实 Go 函数的隔离复现。PASS 代表缺陷已复现，不代表金融语义正确。
- `validation-summary.json`：已执行测试的汇总；前端原始测试输出见 `frontend-tests.log`。
- `evidence-manifest.json`：本次归档文件的实际字节 SHA256。

`*.raw.json` 保留收到数据的 JSON 结构和字段，为方便阅读经过解码再编码。探针记录中的 `sha256` 是当时收到的 HTTP 正文字节摘要，**不等于重编码 JSON 文件的字节摘要**；后者见 evidence manifest。不要混用两者进行文件校验。

特意保留 THS 默认网络路径首次返回去年分钟数据、直连未能复现的矛盾记录。主报告按可复现直连结果提出保守建议，没有将单次异常返回升级为留存承诺。

所有样本只表示当次观察，不承诺固定 QPS、封禁时长、全市场留存范围或未来接口可用性。源数据是公开证券行情；本目录没有 SSH 密码、API Token、Cookie 或账号会话。

`probe_history.py` 只访问少量公开证券接口；按组串行、间隔三秒，连续两次传输/业务失败停止。本次实测已经完成，不需要为了阅读报告再次联网执行。若重新测量，请保存到新的输出目录，保留本次证据。

`run_code_reproductions.py` 通过 Go overlay 注入 `*.go.txt` 中的诊断测试，不把故意复现旧错误的断言加入产品测试套件。运行对象应与报告的 revision 对齐；产品修复之后这些旧问题断言可能失败，这是预期差异。
