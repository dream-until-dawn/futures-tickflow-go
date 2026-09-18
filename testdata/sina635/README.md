# 聚合对账的 fixture：probe.md 6.35 那次取数的**原样字节**

十份都是 **2026-09-18 09:27:15–09:27:47 从新浪线上取回的原始响应**（`docs/probe.md` 6.35「读数」二），
没有删改、没有格式化。6.35 的一次性程序（scratchpad/agg635，不进仓）就是读这十份得出「48 / 48」的。

```
分钟线  https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getFewMinLine?symbol=<SYM>&type=<5|15|30|60>
日线    https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine?symbol=<SYM>
Referer: https://finance.sina.com.cn
```

| 文件 | md5 |
|---|---|
| `AU2612_5m.jsonp` | `57cc36b31d67e75ad5f0e6e8a47359d1` |
| `AU2612_15m.jsonp` | `73c7abaf64e029efddf5bc90a3e69f7b` |
| `AU2612_30m.jsonp` | `81bd248df62fe3596abd4579c1de377a` |
| `AU2612_60m.jsonp` | `c792621bc29d5b1b831229aeb2158f36` |
| `AU2612_1d.jsonp` | `5d13944a6fba4d282a43819fbb286592` |
| `RB2601_5m.jsonp` | `418dd9408f24acc6fce09514ebe28ab5` |
| `RB2601_15m.jsonp` | `d527f81e940c931d0ae1276f2c10d946` |
| `RB2601_30m.jsonp` | `5e207b56f383613f735a81b6508f7513` |
| `RB2601_60m.jsonp` | `49ffe2e26445f3a966af4723403b581f` |
| `RB2601_1d.jsonp` | `8a95199f51f933a5e2e22c55439b652d` |

`TestSina635FixturesUntouched` 钉着上表的 md5：改了字节或换了文件，它先红。

## 它们在这里钉什么、钉不了什么

- **钉**：`AggTradingAxis` 由新浪 5m 聚合出的 15 / 30 / 60m，**标签**（收盘时刻）与新浪自己的高周期逐根相等
  （齐全交易日 × 三个周期 ＝ 48 组；RB2601 9 天、AU2612 7 天）；数值与测试里的朴素参考逐根相等。
- **钉不了数值对新浪**：新浪各周期的**数值**自己就不自洽（6.35：1196 根里 818 根，低周期合成 ≠ 它给的高周期）
  ⇒ 新浪的 OHLCV 不当聚合数值的尺子，只当读数。
- **钉不了 1m ⇒ 5m、钉不了时钟网格**：新浪没有 1m；时钟网格是天勤的口径。两样都只用合成数据测（用户 2026-09-18 裁 U3）。
- **RB2601 那段不是「最近」**：它 2026-01-15 就到期了，新浪给的是到期前最后 1023 根，窗口跨了元旦（6.35「取数与判对的出入」）。
- 交易日归属：同合约日线当交易日表注入 `embedded`，每根按「标签 − 1 毫秒」问 `DayAt`（6.35 第六节）。
