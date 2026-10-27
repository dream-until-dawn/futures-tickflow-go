# 夜盘末根执行准备设计

## 10/27 夜间实际结果：未启动，验收未完成

22:35 的初检及一次全新目录复验均在双来源六次 NTP 采样时因 TimeoutError 退出；随后单次诊断两来源恢复响应，具体 SHFE.rb2701 短连取得 20 帧、最近五根完整分钟身份验证通过，但这不满足完整持续校准门禁。22:49 阶段重新生成的新单再次因 NTP 超时失败，尚未运行正式启动入口，没有 clock-task、capture-task 或正式夜盘录制进程。没有降低条件，没有补启过期窗口，也没有将有限短连当作末根现场样本。故夜盘末根及校准 UTC 时间结论均未建立；超时根因尚未定位，不能据单次恢复断言已修复。

23:07:31 的收尾触发先复核源码、进程及全部失败资料。三次二进制均与干净来源 `0e35666650d35762fb7266a9c186ce683da49697` 的下午绑定产物一致，SHA256 为 `237ea3db0f431e36fbb612a79541dd0d8258d292fb5a2ea37ad84f29563eebb4`。本次完全没有收盘原始帧或持续 clock.jsonl，因此收盘后历史对账不适用，不补造末根 L-C 或四秒保证结论。

三次原始预检目录依次为 `C:\Temp\tickflow-night-precheck-2235-20261027-701e4424ab8a481bacd114c077b87fb1`、`C:\Temp\tickflow-night-precheck-2235-retry-20261027-37f7e10e4d9c48028cc8917bb84d663a`、`C:\Temp\tickflow-night-live-20261027-2249-2f122998a46b42b0b96961fc92835e49`。原件字节未变；失败封存位于 `C:\Temp\tickflow-night-failure-closeout-20261027-8073670db8d24a1b9e8c25d19c6be27d`，13 文件使用 night-preflight-failure-v1 清单，明确分类 incomplete，不表示现场通过。迁移副本校验一致，改坏副本启动失败记录的反例被拒绝；seal.json SHA256 为 `dfe3418646fb958bca43ffa5c5ffdc5ae1213eb17350819974cd1c2f7a87900a`。

本窗口结束后删除自动跟进，不顺延至下一交易日。后续先单独验证 NTP 采样的网络可靠性与失败诊断，再登记新窗口；未经新执行单与全部预检通过，不重新采集。下文设计、下午通过记录均保留其当时范围，不代表本次夜盘通过。

目标是确认具体合约身份，并把本机接收时间与 UTC 偏差分开记录。主连映射不可用时，不能用日盘主连样本补称具体合约。新增显式合约录制参数，直接请求 SHFE.rb2701，凭响应中的原生键及最近五根完整分钟预检身份；不声称它是当前主力。

时钟校准使用两个 NTP 服务的请求/响应时间及服务端接收/发送时间，检查响应来源、模式、同步状态、请求回显、往返、根离散度和单调时钟；记录偏差区间而非单点值。六次采样必须双来源齐全且区间有交集，否则预检失败。该区间仍以 NTP 服务端准确性和非负网络传播时间为前提，不代表交易所时间或行情网络延迟保证。

原始 raw.jsonl 保留本机 recv_ms，不改写。UTC 接收区间按 recv_ms 加对应 NTP 偏差区间解释；现场另一个独立监护任务每 30 秒采样，收尾必须核对收盘附近前后校准点、漂移和跳变。校准任务失败、缺点或区间不一致时，时间结论无效；不修改系统时间或服务配置。

分三阶段：先实现参数、NTP 校验及正反测试；再从干净提交编译，执行有限时长真实具体合约预检和新计划生成；夜盘临近才重新预检并启动。准备阶段不启动长时间现场任务。录制与时钟任务使用独立进程作用域，凭证不写入计划，旧证据不覆盖。

今晚拟用 2026-10-27 22:50—23:07，末根收盘 23:00，监护最晚 23:09；归属 20261028。依据上期所[交易时间](https://www.shfe.com.cn/services/calenderandholidays/tradinghours/)及[休市安排](https://www.shfe.com.cn/services/calenderandholidays/holiday/)，启动前复查临时通知。建议 22:35 唤醒，留出重新预检时间；23:10 复核收尾。正反测试、预检路径及最终提交以实施记录补齐，不把本设计当作已执行证据。

## 操作与门禁

参数存于 [10/27 夜盘单](live-plans/20261027-night.json)。22:35 先核对交易安排、工作区及服务端；22:49 从干净提交生成新的准备目录，真实六秒短连与双来源六次采样通过后，在一分钟内执行启动入口。今天提前生成的计划只用于预检，不可今晚直接复用。若错过 22:56，启动入口将拒绝本窗口。

```powershell
$capturePython = 'C:\Users\沁心\AppData\Local\Python\pythoncore-3.14-64\python.exe'
$env:PATH = (Split-Path $capturePython) + ';' + $env:PATH
$nightPreparation = 'C:\Temp\tickflow-night-live-' + [guid]::NewGuid().ToString('N')
& $capturePython -X utf8 tools/probe/session_end/prepare.py $nightPreparation --spec docs/live-plans/20261027-night.json
if ($LASTEXITCODE -ne 0) { throw '新执行单预检失败，不启动' }
# 只在22:49之后、准备通过一分钟内执行；此前调用会明确拒绝。
& $capturePython -X utf8 tools/probe/session_end/prepare.py $nightPreparation --launch
if ($LASTEXITCODE -ne 0) { throw '启动未成功，先核验新目录，不得重复启动' }
& $capturePython -X utf8 tools/probe/capture_task/cli.py status (Join-Path $nightPreparation 'clock-task')
& $capturePython -X utf8 tools/probe/capture_task/cli.py status (Join-Path $nightPreparation 'capture-task')
```

执行单绑定源码 SHA、二进制、时钟脚本及两个计划的哈希；启动前检查字节和新鲜度。持续校准先启动，实际写出有效双来源证明后才启动行情；两者均使用 independent 作用域，不能退化为 Job 模式。正式行情输出是 capture-task/raw.jsonl，持续校准为 clock-task/clock.jsonl，时间结论要求两任务共同成功。监护总时限在每个任务真正启动时重新按绝对截止计算，避免等待校准把期限顺延。

收尾依次核对实际退出、原生 SHFE.rb2701 键、收盘前正常推进、末根与收盘后历史七字段一致；复查整个 clock.jsonl 的前后覆盖、单调时钟和偏差区间变化，原始文件不改写。报告应同时列本机 L-C 与经校准的 UTC 接收区间，仍不得把接收时间当作服务端改动时间或普遍四秒保证。现场文件按真实材料封存，反例只在副本上执行。

## 10/27 下午实施预检

首次干净候选 76e7e60 的准备产物位于 `C:\Temp\tickflow-night-prepared-20261027-986179057dd54c17a227017f2db50ca2`：15:40:50—15:40:55 的短连取得具体 SHFE.rb2701 原生键，14:55—14:59 五根完整分钟及七字段通过身份预检；不作当前主力声明。双来源六次 NTP 响应校验通过，偏差区间约 [-0.99,-0.71] 秒。提前调用启动入口实际被拒绝，未创建 clock-task 或 capture-task，不会提前录制至晚上。

三模块 gofmt/vet/test、57 项 Python 正反测试、生成器还原演练及 doccheck 通过。其间中文脚本双流编码保护漏项已由守卫检出并修正，未增加豁免。原生合约参数还通过本地 WebSocket 的实际请求键与旧文件拒绝覆盖测试。后续仅改进了具体合约录制结束时的分析命令提示，并补齐本记录；最终来源和远端CI以Git提交及运行结果为准。现场必须重新编译预检，不复用本节旧参数时限。
