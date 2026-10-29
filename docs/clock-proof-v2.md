# 周期间时钟锚点与独立复核

从本轮实现开始，持续clock.jsonl使用ntp-cycle-v2。每周期保存recording_id（同一次记录的UUID）、从0连续增长的sequence，以及anchors中的wall_start/monotonic_start/wall_end/monotonic_end。measured_at、cycle_started_at、cycle_seconds与锚点逐值一致，不能从重新启动的进程拼接周期。

诊断文件逐条刷新record_anchor、样本成功/失败、cycle_pass或cycle_failed；正常到期才追加唯一record_end。周期间跳变保存当前与前一墙钟/单调起点后拒绝，不仅写一句错误。周期完整性、双来源六样本及所有原质量门禁不变。

独立工具tools/probe/session_end/review_clock.py不导入clock_probe，直接从NTP原始字节重算区间、RTT和根误差；检查连续序号、周期内与相邻周期墙钟/单调增量差不超过50毫秒、单调不倒退、不重叠、起点间隔不超过65秒。默认还读取同名-attempts.jsonl，逐样本核对证明、拒绝失败或跳变记录，要求唯一正常结束标记。只有部分有效周期、缺结束诊断或少一个周期均不能冒充完整记录。

```powershell
python -X utf8 tools/probe/session_end/review_clock.py C:/Temp/example/clock.jsonl --begin 2026-10-29T14:55:00+08:00 --end 2026-10-29T15:00:00+08:00
```

可选覆盖起止须同时给出，要求窗口之前和之后均有完整周期。省略时只验证输入记录的结构、样本及结束，不宣称覆盖任何交易窗口；只有单周期时明确标为不适用周期间比较。

已封存的10/28、10/29旧现场没有这些锚点，严格v2复核会拒绝，不改写、不升级其原有证据。该修复只对新记录生效。本轮以离线合成正例及正负跳变、缺锚点、非有限、倒退、拼接、丢周期、伪造区间、坏包、缺正常结束等反例标定，没有新增现场采集。

锚点只能验证本机墙钟与单调时钟的一致性，不能独立证明NTP服务端准确、直接上游路径独立或校准点间振荡器精度。UTC接收区间仍须明示网络与邻点包络假设，不代表交易所最终修改时间或普遍四秒保证。
