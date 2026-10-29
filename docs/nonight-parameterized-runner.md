# 停夜盘参数化执行与边界

执行入口仍为tools/probe/nonight_preflight.py，新增必需的--plan；不提供计划就拒绝旧固定日期。Go现场入口独立解析同一份JSON并核对SHA256、具体SHFE.rbYYMM、四个有序交易日、公告停夜日、恢复日和阶段窗口。注入日历、Live起点、原始帧计数及历史查询区间全部由计划派生，不再引用9/30、10/8常量；这些常量只保留在历史离线标定里。

## 执行单

形状见 [合成示例](../tools/probe/testdata/nonight-synthetic/plan.json)。schema为1；trading_days按节前两日、恢复日及其后一交易日排列。no_night_after是公告中明确说晚上不交易的日期，resume_day须为日历第三项。evening_begin/end固定绑定停夜日20:59—21:10，fetch_begin/end固定绑定恢复日15:10—15:15；时刻须分钟对齐并显式+08:00。并非任意选四个工作日就证明官方交易安排正确。

official_url仅接受上期所HTTPS域名；evidence_file相对执行单目录或为绝对路径，evidence_sha256绑定文件原始字节。工具校验域名、文件与哈希，不自动证明公告真实性或解读日期；实际登记前须独立查阅原公告。evidence_class为synthetic的计划默认拒绝；--allow-synthetic只校验形状，不输出实跑命令。仓内示例是已过期合成标定，不代表存在未来官方窗口；不能仅把它的class改为official后实跑。

正式预检先核对仓外不存在的输出目录、凭证来源（环境或仓库根.env，不输出值）和未过期窗口；输出命令绑定计划路径、计划SHA及阶段参数。实跑再次独立校验，醒晚30秒不联网；独占创建目录，保存execution-plan.json与official-evidence.bin，不复用空目录，不覆盖旧证据。行情连接和读取受绝对截止约束。不涉及下单或资金。

```powershell
# 仅演示离线形状验证；使用合成凭证存在性标定时不代表真实鉴权。
python -X utf8 tools/probe/nonight_preflight.py --phase evening --ins SHFE.rb2701 --out C:/Temp/nonight-synthetic-unused --plan tools/probe/testdata/nonight-synthetic/plan.json --allow-synthetic
```

真窗口登记后用同入口去掉--allow-synthetic并换成独立核实的官方计划；分别给evening/fetch不同的新目录。阶段甲保留原始帧、注入与不注入两路Live对照，阶段乙拉历史并按派生区间计数。运行成功不等于业务验收成功，仍按probe.md 6.41独立复核对照是否触发、连接错误与判定错误、零根与节后正向恢复。

L9的trading_day_end_id须在恢复日盘中（例如09:05后、15:00前）另保留原始节点；既有shinny-suspended-night-span探针按实际节点识别普通夜/停夜，无固定9/30日期。本次未把15:10历史查询当成盘中预测读数，也未改L9生产判据。官方未来特殊窗口及真实Live/L9证据仍缺失，发布继续阻塞。

同日后续已补原始取证及超时链：该探针现在必须提供-day-node-out仓外新目录，用review_daynodes.py独立重放；详见 [L9取证链](l9-evidence-chain.md)。完整节点集合不等于业务对照通过，仍须原探针的控制值判据及真实特殊窗口。
