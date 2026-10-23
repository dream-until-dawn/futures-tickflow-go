# v0.11 adapter 与停夜盘验证推进记录

当前状态（2026-10-23）：使用者已取消外部评审并授权直接操作 Git；adapter 已随 a4737fe 合入远端 main。[review-20261023.md](review-20261023.md) 与下面的 9/29 结果作为历史证据保留；新的真实行情排期、运行器和实测进展见 [live-validation-plan.md](live-validation-plan.md)。

10/23 后续补强基线 23f59b0：真实固定版本模拟器录入合成多/空各两手，转换后的 Close 推进产生 +2250/-2250 的手算盈亏；非法价格根拒绝后账户不变。adapter 完整 test/vet 通过，突变从七项增至八项，全部 KILLED，包含“误用 High 计价”使多/空变成 +5000/-5000 的断言失败；还原后通过，克隆 before/after 均为 ""。这项测试使用 ApplyTrade 直接录入，不宣称 Submit、撮合或真实行情成交闭环通过。

晚间补充收口：封存225根真实历史经转换连续推进模拟器、多/空一手最终-350/+350及错误交易日账户不变，见 [adapter-recorded-history.md](adapter-recorded-history.md)；独立外部模块按远端明确提交取用、无本地replace，见 [adapter-external-consumer.md](adapter-external-consumer.md)。按使用者晚间“允许自行合并主线”的新增授权，同步生命周期主线后，97f6d7c完整自检通过，补充收口验收完成。它们不关闭现场行情缺口，不创建发布tag，不扩展撮合或结算职责。

## 2026-09-29：实现前对表与分期

用户同意先推进 adapter 对接与 9/30 探针准备；合并 main、打 tag 和实盘取数仍在对应确认点执行。

本仓起点为远端 main `4d0b09a`。兄弟仓远端 main 为 `6254e468a7da44519d3a94a92fb2c4e076e873ec`；F9b 实现 `be82b5f` 已由 `6d3c7bbf33477e72fddbb625aaed91149a5d689d` 合入，祖先检查通过。以该合并提交作为 adapter 的固定依赖，不使用兄弟仓工作区 replace。该版 Advance 只做守卫和 Close 计价，不撮合。

阶段一：复核 design.md 12.7 的字段映射，新增独立 adapter Go 模块；主模块不引入模拟器或 decimal。函数仍为 Bar(b tickflow.Bar, sym tickflow.Symbol) (futsim.Bar, error)。毫秒转换成固定东八区时间，TradingDay 原样映射，价格用 decimal.NewFromFloat，三个消费价格的 NaN/Inf 一律报错。合约按字段映射；非法或非规范 Symbol 拒绝。价格关系、交易日与时段的业务校验由 Advance 负责，adapter 不复制其规则。

阶段二：先写测试再实现。覆盖六所标识、郑商所三位线格式、跨午夜交易日、毫秒精度、历史探针价格样本、三个价格字段的 NaN/正负 Inf、非法合约。集成测试直接调用固定版本的真实 Advance，使用独立合成日历；正常时段末根通过，跨休市、错误交易日、坏 OHLC 被拒且账户不变。另做孤立副本上的受控突变，确认关键断言确实能变红。

范围：只转换未复权且落在同一个连续交易时段内的根。日线或跨休市的聚合根即使可转换，也可能被 Advance 拒绝；不拆根、不推算缺失价格。FlagPartial 原样接受但无法传给下游，调用方决定是否拦截。Open/成交量/持仓量/结算价不参与转换，不触发 Settle。

阶段三：9/30 探针只做离线准备，保留 probe.md 6.41 的冻结判据。整理 PowerShell 运行命令、凭证存在性与运行参数预检，预检不联网、不打印凭证；实跑仍需确认具体合约、输出目录与时间。L9 的 9/28 窗口已过，现有探针读最新节点，不能用今天的节点冒充昨天读数；无原始录制时安排 10/8 09:05–15:00 内另行确认，读数前不实现 L9。

阶段四：修正当前能力和排期状态，运行全模块检查、文档检查和生成器还原演练，记录真实结果。历史记录与已发布说明保持原样，新增更正明确当前进度。

## 2026-09-29：本批交付与验证

实现、测试与预检位于功能分支 codex/v011-adapter-preflight，尚未合并主线或打 tag。adapter 的用法与边界见 [adapter/README.md](../adapter/README.md)，实盘执行准备见 [nonight-runbook.md](nonight-runbook.md)。

固定依赖通过直接源码下载与校验数据库验证；最终 go.mod/go.sum 中没有本机模拟器路径。`go list -deps .` 的输出中没有 decimal 或 futures-position-simulator，主行情模块的依赖隔离成立。

实现前，在临时返回错误的 Bar 上执行 `go test ./... -count=1`（adapter 目录）：退出 1，合约、时间、价格、非有限输入与 Advance 集成测试失败。这是开发过程的先红读数，不冒充干净提交上的发布基线。实现后同命令通过。

文档登记先红：修改 CONTRIBUTING 的模块数后，执行 `go test . -run 'Test.*(Rules|Quoted|Census|Status|Table)' -count=1`，退出 1，报旧的“本仓有两个”缺失、新的“本仓有三个”未登记；随后才重建 docs_test.go。

受控突变测量：`python tools/audit/adapter_mutations.py`，底座 a4a7f2c0e1bbbc42f42643c1a98674c390fa9e3a，在临时 Git 克隆执行。克隆开始和恢复后的 `git status --porcelain` 均为空，baseline_rc=0、restored_rc=0；七项均 KILLED，SURVIVED=0、UNRESOLVED=0。后续到 c8ca63b 的 adapter 目录 diff 为空。

| 突变 | 检出测试 | 结果 |
|---|---|---|
| 交易日加一天 | TestBarTimeAndPrices | 退出 1，交易日断言失败 |
| End 错取 Ts | TestBarTimeAndPrices | 退出 1，时间断言失败 |
| 合约月份加一 | TestBarSymbols | 退出 1，合约映射断言失败 |
| Close 错取 High | TestBarTimeAndPrices | 退出 1，高低收断言失败 |
| Close 擅自保留两位 | TestBarRecordedPrices | 退出 1，价格与长尾断言失败 |
| 取消非有限价格拒绝 | TestBarRejectsNonFinite | 退出 1，NaN 进入 decimal 导致 panic，违反应返回错误的契约 |
| 取消非规范合约拒绝 | TestBarRejectsInvalidSymbol | 退出 1，非法合约被接受 |

以上是本批受控突变，不是独立外部评审，也不声称覆盖所有可能的破坏。编译失败不计 KILLED，脚本要求指定测试实际失败。

全量检查期间先发现并修复两类工具问题：Python 脚本未同时包裹 stdout/stderr 的 UTF-8；文档中筛选正则被测试名守卫当成完整函数名。另将本次编辑的三个文档从 CRLF 还原为属性规定的 LF；内容 diff 为空后刷新索引状态，重新取得前后干净的正式基线。

最终正式复验底座 c8ca63b，命令和输出摘要如下。命令前后 `git status --porcelain` 均为空；失败即中止链条，没有把失败后的其它成功命令当成总成功。

```text
python tools/audit/module_sweep.py
  .                   gofmt ok / vet ok / test ok
  adapter             gofmt ok / vet ok / test ok
  tools/probe/shinny   gofmt ok / vet ok / test ok
  rebuild_docs_test.py --drill  ok
  演练前后工作区内容指纹 相同（e3b0c44298fc）
go run ./tools/doccheck
  文档声明 203 处；源码声明 683 处；白名单 0 项；一致
python -m unittest discover -s tools/probe -p test_nonight_preflight.py -v
  Ran 3 tests / OK
```

尚未完成：真实行情验收、主力映射确认、L9 停夜盘日字段读数、主分支合并和版本发布。免鉴权公开合约表本次超时；SHFE.rb2701 仅作为预检候选。9/30 与 10/8 实跑均未启动，也未建立定时任务。价格样本只复核文档可重建的 16 项，末尾零按 decimal 的规范字符串处理；没有补造原 29 项的缺失输入。
