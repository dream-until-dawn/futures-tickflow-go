# 行情根到记账内核

独立 Go 模块，包名 adapter。主行情模块不依赖 decimal 或记账内核。
模拟器固定在已合入 F9b 的版本 v0.0.0-20260923031436-6d3c7bbf3347；该版本 Advance 只做守卫和计价，不撮合。

```go
converted, err := adapter.Bar(rawBar, symbol)
if err != nil {
    return err
}
_, err = simulator.Advance(converted)
```

输入须为未复权价格。函数按字段映射合约、保留交易日，将毫秒转为固定东八区时间，将有限的 High/Low/Close 转为 decimal。非法或非规范合约、任一消费价格为 NaN 或 Inf 时返回错误。

业务校验在 Advance：必须是正时长，价格满足 Low > 0 且 Low ≤ Close ≤ High；配置日历后，整根必须落在一个连续时段里。因此日线、跨休市的聚合根不能直接作为盘中输入。adapter 不拆根、不自动结算、不计算金额。FlagPartial 不拦截；调用方必须自行决定是否接受不完整根。Open/Settle/Volume/Turnover/OpenInterest/Flags 不传递。

主连的 RawClose 只恢复收盘价，不能把复权后的 High/Low 与它拼成一根输入；请按当日实际合约读取完整未复权 OHLC。

仓内开发使用 go.mod 的 replace 指向父目录；外部使用者要显式选择所需的行情模块版本（依赖模块内的 replace 不会被继承）。

10/29离线消费验证：仓库外无replace消费者通过Go模块服务取得adapter固定提交2f02b0f，解析为v0.0.0-20261029074532-2f02b0faa082，默认根模块v0.10.0时转换与NaN拒绝通过；显式升级根模块至同候选伪版本后也通过。并未创建adapter或主模块的发布标签。依赖的记账内核仍为上述固定版本，未升级。完整边界与API差异见 [v0.11草案](../docs/release/v0.11.0.md)。

验证：在本目录运行 `go test ./... -count=1` 和 `go vet ./...`。根目录的 `python tools/audit/module_sweep.py` 会自动发现本模块。

测试使用独立合成日历和真实模拟器 API。集成测试验证转换、时段、交易日拒绝路径及合成持仓 Close 计价，不宣称真实行情对拍或成交闭环。价格样本为 probe.md 十明确留下的 16 项；旧的 29 项一次性脚本未入仓，未声称重现全部 29 项。

10/23 补强：在固定版本模拟器中直接录入合成多、空持仓，再将 adapter 转换后的 Close 推给 Advance，核对手工计算的盈亏与非法根失败后的账户不变。录入采用 ApplyTrade，避免把计价集成测试冒充 Submit 校验或撮合验收；规则乘数与费率明确为合成输入，不升级依赖。两手、开仓价 100、Close 101.125、合成乘数 1000 时，多/空盈亏分别为 +2250/-2250；OHLC 非法根拒绝后账户不变，两格通过。
