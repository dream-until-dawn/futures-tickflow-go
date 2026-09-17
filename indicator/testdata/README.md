# indicator 的 fixture：**真实响应，不是手写的**

三份都是 **2026-09-17 22:59–23:00 从新浪线上取回来的原样字节**（`docs/probe.md` 6.33 那次取数落的盘），
没有删改、没有格式化。端点与 `source/sinasource/testdata/README.md` 里记的是同一个：

```
https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine?symbol=<SYM>
Referer: https://finance.sina.com.cn
```

| 文件 | 合约 | 根数 | 首日 | 末日 | md5 | 它在这里是为了钉住什么 |
|---|---|---|---|---|---|---|
| `daily_RB0.jsonp` | 新浪主连 `RB0`（**未复权**） | 4246 | 2009-03-27 | 2026-09-17 | `f9664522…` | 长序列：递归类指标在它上面**量得出**逐位收敛点（settle_test 的基线） |
| `daily_RB2501.jsonp` | `RB2501`（已到期） | 242 | 2024-01-16 | 2025-01-15 | `d45d3421…` | 单个合约的整段日线：递归类指标在它上面**量不出**收敛点（6.33 甲）；golden 的数据 |
| `daily_RB2412.jsonp` | `RB2412`（已到期） | 238 | 2023-12-18 | 2024-12-13 | `9b143df3…` | 前两根收盘相同（3889 / 3889）：measureSettle 的**退化格**（6.33 读数第三节） |

`TestIndicatorFixturesUntouched` 钉着上表的根数、首末日与 md5：改了字节或换了文件，它先红。

## ⚠️ 这几份数据**没有**的形状

- **锁板**：三份里 `H == L` 的日线一根都没有。锁板段用合成数据测（用户 2026-09-17 定：只用合成数据，
  理由见 `docs/design.md` §十五「v0.8 起手」开工条件三）。
- **复权**：`RB0` 是新浪拼的主连，**未复权**。settle_test 用它量收敛点，射程只到「RB0 未复权」（起手节 戊）。

## 更新它们时

重新取一次就行，**取回来之后把上表的数与测试里钉的 md5 一起重量**；golden.csv 要 `-update` 重生成并说明原因。
