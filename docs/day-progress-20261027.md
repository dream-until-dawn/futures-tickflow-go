# 2026-10-27 现场验收启动记录

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
