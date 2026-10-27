# 2026-10-27 现场验收启动记录

## 15:11 收尾：主连日盘末根观测与同序列历史对账通过

15:11:01 复查录制已 completed、returncode=0、stalls=0；run.log 明确记录 14:53:53.850 开始，15:07:00.000 断开，共 1363 帧、422510 字节。末帧接收时间为 15:00:01.700，不能把末帧当断开时刻；收盘后静默与录制日志的实际结束分别记录。原始 raw.jsonl SHA256 为 `335cc883d8978193adbee798dca327512500c1080179da6271f7d3dcb4dc8899`。

来源仍为下文 79d17b5 的固定 probe.exe。独立 Go 末根报告与额外 Python 重放一致：收盘前四根正常分钟推进；14:59 末根首次接收于 14:59:01.151，113 次实际字段变化，最后变化接收于 15:00:01.135，本机 L-C=1135ms。本份记录尚未出现超过四秒的本机修改，但不推广成普遍保证。

在仓库外复制探针源码并加入只读收盘后历史查询探针，源码保留在证据根目录的 history-tool/postclose_test.go 中；这是封存的临时工具，不是仓内可点名运行的测试。收盘后新连接取得同一 KQ.m@SHFE.rb 序列 14:55—14:59 五根，原始响应另存 post-history-raw.jsonl。五个标签及 O/H/L/C/V/close_oi/open_oi 七字段与现场最终快照逐项一致。首次副本构建因相对 replace 指向 C:\go.mod 失败，修正仅临时副本的本地模块路径后实际 PASS，前后日志保留。临时扩展工具单独封存，不将其冒称为原录制二进制。

真实材料副本的三项反例均在实际 Go 报告入口失败：删去收盘后帧导致覆盖不足；注入 C+5001ms 的真实字段修改触发超四秒 FAIL；破坏 JSON 被拒绝。原 task 目录前后哈希一致。收尾 NTP 复测 server-local 约 -0.907/-0.909 秒，往返约 0.252/0.314 秒，与开头约 -0.934/-0.936 秒方向一致，仅为两端偏差估计，不证明全程准确度或网络延迟上界。

前后 openmd 映射均超时，原始 quote 也未提供 underlying_symbol，故具体合约归属未建立；不能把本次主连观测当作 SHFE.rb2701 末根实测。日盘状态更新为“主连观测、同序列历史对账已通过；具体合约归属及四秒保证未建立”，夜盘末根、停夜盘 Live/L9 仍未关闭，未新启动夜盘或发布 tag。

证据根目录仍为下文 C:\Temp\tickflow-sessionend-20261027-63db2f0f762646a5b73e28843631a601。session-acceptance-report.json 记录完整结果；sealed-sessionend 使用单独的 sessionend-observation-v1 清单封存 42 文件，分类明确保留 mapping_unresolved，不复用只适用于长缺口的封存格式。sealed-sessionend-copy 迁移副本全部文件哈希一致，两份 seal-sessionend.json 的 SHA256 同为 `886856b363a79f9890d4e920c40f9891aebde7227192f953fba961ba07674b6d`。

## 14:53 启动日盘末根独立录制

使用者在 14:52 唤醒后，14:52:49 核对时间，仍可覆盖收盘前至少两根正常分钟推进与 15:00 末根；不把实际起跑追记为原定 14:50。独立探针的 SessionEnd、TailDeadline、LiveTailSendsOnlyMarketAids、OutputInsideRepo 相关测试通过，从干净来源 `79d17b52e6e7ef1d8e320a7f1c7d93451584ee3b` 编译 probe.exe，SHA256 为 `04d31a5ff0395a33aecefaac5ba40f55779d3df576c747fc146c05663e89bb92`。

新证据目录 `C:\Temp\tickflow-sessionend-20261027-63db2f0f762646a5b73e28843631a601` 保存二进制、来源、参数计划和 manifest；task/raw.jsonl 为原始帧，task/run.log 为录制日志。监护为 independent，PID 20820、子进程 2304；实际日志开始时刻 14:53:53.850，录制墙钟截止 15:07，监护上限约 15:09。启动后状态 running，raw.jsonl 已超过 12 KiB，心跳和写入均实际更新，尚未作收盘结论。

本次仍为 KQ.m@SHFE.rb 主连观测，只发鉴权、名称服务及行情请求。公开映射取数前后快照由原探针异步记录，取得前不声称具体合约。15:00 后 raw 通道允许静默，避免把正常收盘后无更新判为停滞；完整性仍须核对实际结束日志、断开时刻及文件。末根分析不等于完整历史对账。

14:53:52 的两个只读 NTP 查询显示 server-local 约 -0.934/-0.936 秒，往返约 0.275/0.336 秒；未更改系统时钟，仅作为当次偏差估计，不作全程准确度保证。收尾仍需复测并将时钟偏差和网络接收延迟纳入解释，不能只凭本机 L-C 小于四秒认定真实四秒指标通过。原 tickflow-17 自动化未恢复，建议 15:09 人工唤醒复核。

## 11:20 收尾：本次长缺口现场验收通过

11:20:50 北京时间复查，任务状态 completed、returncode=0、stalls=0，采集运行器 TestLiveLongGapReal 实际 PASS，11:18:10 正常结束。独立复核器对原始参照、Live 输出与结束后历史逐根复核通过：48 根输出、9819 帧参照、32 根完整断流分钟；实际 socket_closed=10:36:00.000，resume_next=11:08:00.064（均为本机北京时间），间隔 1920.064 秒。运行器报告推送 2、握手 3；这些计数仅描述本次执行。

独立审计报告位于证据根目录的 independent-review.json、acceptance-report.json。四份原始 JSONL 的分析前后 SHA256 完全相同；来源仍为下文绑定的 82f8949，不因后续文档提交改写。原任务已写入 seal.json，11 个文件封存核验通过；sealed-copy 迁移副本也通过相同独立复核与 11 文件字节校验。

三组真实材料副本反例均被独立分析及封存校验拒绝：篡改 Live 首根收盘价、删去历史末根、删去 socket_closed 事件。反例只写入 negative-* 副本，原始任务前后字节一致，反例之后再次校验原件通过。acceptance-check.py 保留了本轮复核步骤，不读取凭证，不使用合成材料开关。

本结论只关闭 SHFE.rb2701 在本次 10:30—11:18 窗口的完整长缺口现场条目，不证明其它品种、其它窗口都通过；10/23 与 10/26 未验成记录保持原状。日盘末根、夜盘末根、停夜盘 Live/L9 仍需另行现场验收，v0.11 尚未发布，不打 tag。

使用者要求在日盘期间继续推进，沿用已授予的现场采集及主线合并权限。10:22:33 北京时间核验处于上午休息段；按上期所[交易时间](https://www.shfe.com.cn/services/calenderandholidays/tradinghours/)及[休市安排](https://www.shfe.com.cn/services/calenderandholidays/holiday/)，选择当天 10:30—11:30 连续段，未查到改变本窗口的临时休市通知。

## 旧请求核验

10/26 准备目录仍在，但预定任务目录 `C:\Temp\tickflow-oct26-morning-task-4966769bc54c471b8e79c3ca06d2d8ec` 不存在，也未生成 launch-plan.json。当前进程枚举未见 python/capture.test 旧采集进程。不将这些现象推断为昨天未启动的具体原因；10/26 未取得现场验收证据，过期单不再执行。

## 新单与预检

证据根目录：`C:\Temp\tickflow-oct27-acceptance-619cc8cd79a54d40891ad4ef0c63727f`。spec.json 为新参数；prepared 为首轮编译预检，launch-prepared 为启动前重新生成的产物与计划，task 为独占任务目录。全部保留，不覆盖历史材料。

固定合约 SHFE.rb2701；DAYS 为 20261026,20261027。北京时间 START 10:30、BEGIN 10:31、DROP 10:36、RESUME 11:08、END 11:18、DEADLINE 11:29。断流 32 分钟，必须按实际事件复核至少 31 根完整分钟；END 后观察十秒并历史对账，不能只用计划时差认定成功。

干净编译来源 `82f89498cdd235b98d093123d7667b68855ebd7d`；实际二进制 SHA256 `465a57ef185b12d0d3519a3ea95b2eefbcd62aad13ddb26b7d6b9deb14c398fd`；启动计划 SHA256 `cc402329ca09c2fd170878980dfcbc35fc18670b02e83f5bcce44317020f97b2`。真实二进制 TestGapPlanOfflinePreflight 通过，11 项执行单正反测试通过。

首轮市场预检因 PowerShell 未引用带点参数导致参数解析失败；第二轮因 PowerShell JSON 自动转日期而使 RFC3339 环境值失真，校验器拒绝。失败日志均保留。第三轮使用显式字符串及引用参数，TestGapMarketPreflight 实际 PASS：鉴权、名称服务、SHFE.rb2701 最近结束段的五根历史完整，末标签为当日 10:14。公开主连映射查询超时，本次不声称当前主力。凭证不落入执行单。

## 启动状态与复核边界

10:24 左右通过 CLI 直接启动，process_scope=independent；返回 supervisor_pid=9124，状态复查显示 child_pid=5628、waiting、心跳更新、stalls=0，run.log 已出现 TestLiveLongGapReal。BEGIN 前没有行情文件活动属于等待状态，不算采集成功，更不算验收通过。

后续先复查双通道活动及参照通道在断流期间持续记录，11:18 后等待运行器完整结束，再执行下列只读独立审计。若异常则保留并按 incomplete 封存；不补造现场资料，不改写已发生的时刻。

```powershell
$capturePython = 'C:\Users\沁心\AppData\Local\Python\pythoncore-3.14-64\python.exe'
$liveTask = 'C:\Temp\tickflow-oct27-acceptance-619cc8cd79a54d40891ad4ef0c63727f\task'
& $capturePython -X utf8 tools/probe/capture_task/cli.py status $liveTask
# 以下仅在运行器完整结束后执行；不传 --allow-synthetic。
# & $capturePython -X utf8 tools/audit/review_long_gap.py $liveTask
# & $capturePython -X utf8 tools/audit/evidence_bundle.py seal $liveTask
# & $capturePython -X utf8 tools/audit/evidence_bundle.py verify $liveTask
```

昨日只读 NTP 测得本机约快 2.4 秒，今日尚未重测，不据此推断今日偏差。当前不采纳四秒末根结论；本单只验长缺口，日盘末根、夜盘末根、停夜盘 Live/L9 仍未关闭。原 tickflow-17 自动化保持暂停，未创建新的自动通知。
