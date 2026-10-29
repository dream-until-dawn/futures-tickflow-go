# 9/30 停夜盘验证执行准备

10/29离线工具更新：现行入口已参数化并要求绑定执行单及公告证据，见 [nonight-parameterized-runner.md](nonight-parameterized-runner.md)。下文全部保留旧窗口历史，旧命令不再直接可用；没有新增未来官方窗口或现场完成结论。

2026-10-23 状态：以下为已过期方案的归档，没有完成证据，不再作为当前执行单。新的工作顺序与取数条件见 [live-validation-plan.md](live-validation-plan.md)。旧工具有固定日期，不能只改命令时间复用；预检应拒绝过期窗口。

状态：2026-09-29 离线准备完成，未启动实盘、未安排后台任务。判据仍以 probe.md 6.41 为准，不改冻结判据。

## 待确认的执行参数

- 甲：2026-09-30 20:59:00 至 21:10:00，东八区；三路行情连接（原始录制、注入日历 Live、不注入对照 Live）。
- 乙：2026-10-08 15:10:00 至 15:15:00，东八区；历史行情复核，使用与甲相同的具体合约。
- 合约候选：SHFE.rb2701，来自旧录制，尚未核实 9/29 主力映射。今日读取免鉴权公开合约表 https://openmd.shinnytech.com/t/md/symbols/latest.json 超时，没有得到映射；不能把候选写成今日已确认主力。需补一次有效查询，或由使用者明确选定具体合约。
- 输出位置：用仓库外的新目录；甲乙分别使用不同的空目录，避免历史证据被覆盖。目录路径由预检命令传入，预检本身不创建目录。
- 只用行情通道，不涉及下单、资金或交易操作；具体时刻、合约、目录确认后才执行。

## 离线预检与命令生成

在仓库根目录用 PowerShell 运行：

```powershell
python tools/probe/nonight_preflight.py --phase evening --ins SHFE.rb2701 --out "$env:TEMP/tickflow-nonight-20260930"
python tools/probe/nonight_preflight.py --phase fetch --ins SHFE.rb2701 --out "$env:TEMP/tickflow-nonight-20261008"
```

上面的合约是候选值，补核或使用者选定后替换。输出是待确认的 PowerShell 命令，不会自动执行。凭证优先从进程环境读取，其次从仓库 .env 读取；只报告缺失键名，不输出值。今日预检通过表示键存在，不表示真实鉴权已通过。

运行前重新预检，核对机器时钟与东八区窗口，确保开始前已启动测试；测试若醒晚超过 30 秒会拒绝联网。终端与机器须保持运行，不能把已经输出命令当成已经排程。实际运行前记下提交 SHA 与 git status --porcelain；确认退出码及测试没有 SKIP。

运行完成后保存原始输出；录制结束、分析之前、分析之后分别计算录制文件 MD5。按 6.41 区分判定层、连接层和调用方错误；连接错误不算修复失败，对照未触发时不能判修复通过。正式验收还要等 10/8 的日盘正向证据，9/30 晚的零根不能单独证实停夜盘。

## L9 缺失窗口

6.42 的 9/28 取数仍未在仓内找到结果。本次未搜索仓外录制文件，因此不声称没有录制。现有 dayNodes 只取最新序列节点，9/29 不能直接补回 9/28 的服务端预测字段。先核对是否有原始录制；若没有，10/8 09:05 后、15:00 前另行确认盘中取数。10/8 的 15:10 历史复核不能代替这次盘中节点观测。L9 代码继续等待这项读数。

## 本次离线验证

`python -m unittest discover -s tools/probe -p test_nonight_preflight.py -v`：3 个测试通过，覆盖有效配置、缺凭证、错误合约、仓内路径与已有证据目录。

`go test ./source/shinnysource -run 'NoNight|PostHoliday' -count=1 -v`：离线用例通过；TestNoNightRealEvening 与 TestNoNightRealFetch 因未设置实跑参数 SKIP，未联网。
