# 夜盘末根执行准备设计

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
