# 用户两门槛机器接口 v1

> 2026-11-17更新：用户取消实际最低开仓作为回测硬项，新评估采用[v2](user-data-gates-v2.md)。本页保留v1协议及阶段说明，旧包不改写。

2026-11-16。与旧严格金融包分开消费，暂不改变生产任务、不打tag。最低手数已由用户确定为实际最低开仓手数，包含已生效专项调整。

## 格式约定

UTF-8 JSON，顶层 `schema="user-data-gates-v1"`；示例含 `synthetic=true`，**只能用于接口开发，禁止当真实数据或验收通过包**。真实包必须显式为false。示例位于 `tools/research/status/examples/user-data-gates-v1/manifest.json`。

顶层 `policy` 含 `baseline_year`、`latest_completed_day`、`current_trade_day`、`minimum_lots_kind="effective_min_open"`。日期均为整数YYYYMMDD；带时刻字段必须含时区，未知为null，不能取文件修改时间补齐。

顶层 `products[]` 每项含 `key`（如SHFE.rb）、`name`、`gates` 和 `bonus`。`gates.backtest.hard_items` 仅含 `history_ohlc/current_margin/effective_min_open`；`gates.daily_signal.hard_items` 仅含 `settlement/daily_update`。硬项含 `status`、`scope`（具体合约字符串数组）、`evidence_ids`、`reason`。门槛含 `eligible_contracts`、`blocking_reasons`；状态只用pass/pending/not_met。通过范围为相关硬项适用合约交集；不能用甲合约的保证金和乙合约的最低开仓量拼成通过。

`history_ohlc` 另含 `coverage`：`first`、`last`、`listing_day`、`artifact_ids`、`valid_days_by_year`。历史为同品种跨到期合约的价格序列；不要求当前合约在2021年就已存在。`scope` 是实际历史具体合约，不是准入当前合约。`listing_day` 仅有直接上市证据时填值。起止或年度有值统计不是连续全量证明，缺日和来源差异必须随包说明，不以首尾两条记录认证长期覆盖。

`current_contracts` **位于顶层**，每项有 `product_key/symbol/source_native/source_snapshot_day/as_of_trade_day`，其中`symbol`使用明确映射后的四位年月；郑商所三位原生代码须有显式年份映射依据，不能就近猜年。保证金放在`margin`：`long/short`为十进制定点字符串，`unit="ratio"`，`basis="exchange_spec"|"reference"|"company"`，`evidence_ids`。10%必须表示为`"0.10"`；参考值不能自动升级为实际下一交易日有效值。

最低开仓字段为 `effective_min_open_lots`（正整数或null）、`minimum_lots_kind`、`special_adjustments_reviewed`、`minimum_evidence_ids`。普通最小报单另记`ordinary_min_order_lots`，不能回填最低开仓空值。审核只适用于明确交易日、合约和适用主体范围；真实客户受限情况另核。

## 原件与行情序列

顶层 `artifacts[]` 每项含 `id/role/path/bytes/sha256/source_url/source_day/retrieved_at/version`。路径相对manifest，禁止绝对路径和向上越界；先核字节数和SHA再解析。`role="daily-records"` 为行情JSONL，`role="source-original"` 为原始响应；硬项`evidence_ids`引用artifact id，不是可变路径。闭环清单应包含所依赖全部原件；公开摘要仅有本地路径时不冒充可迁移完整包。

行情JSONL每行固定小写字段：`product_key`、`symbol`、`source_native`、`day`、`fields`、`source_id`。`fields`中的`open/high/low/close/settle/volume/open_interest/turnover`为十进制定点字符串或null；列不存在、未知和0不互相替代。`day`为所属交易日，不从接收自然日猜测。`source_id`引用原件artifact id。相同合约/日期的不同来源分开保留，不能无声覆盖；EDB缺结算列时不能以收盘价补结算。

结算序列使用同一个`daily-records`角色；逐日信号的`settlement.evidence_ids`指向含目标交易日真实settle的行情序列及原件。`daily_update.evidence_ids`引用日更入口/执行回执等有摘要的资料。其scope不借用保证金或最低开仓集合。日更能力和运行成熟度分开：多日自动运行记录为加分项，不新增第六个硬项；实际一次运行失败不得发布旧日冒充目标日。

## 回测情景与补充项

当前参数用于历史回放时，输出必须标注“当前参数情景”，不声称历史真实保证金、专项门槛、手续费或PIT。乘数等策略所需但非本次硬项的数据，明确采用已有规格或显式假设。`bonus` 保存日历、历史制度、生命周期、PIT、对账及运行实测；不得把这些缺口重新塞回用户两道数据门槛。数据条件通过不代表盈利、真实成交、账户可交易或生产部署获准。
