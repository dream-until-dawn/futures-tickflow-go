# 新门槛离线价格对照包

2026-11-17。接续主线59dde35，v2资料规则已按用户要求取消最低开仓硬项。下一步把两个具体品种已有价格和当前参数整理成小范围可迁移消费候选：RB保留旧v1的真实最低开仓对照，HC保留实际最低开仓null，用于确认新版不会重新要求此项。

不联网、不修改v1原件、不配置模拟数量、不注册或部署任务。当前参数对应17日、目标已收完整原件行情16日。采用原交易所投机比例，不能声称历史真实保证金或账户实收。

来源链先验证冻结v1父包的manifest SHA和每件原件，再从项目已盘点SHA目录读取HC日线；仅收具名具体合约，不拼主连。OHLC基本无效行单列剔除；重叠同值只取一次，源版本冲突停止。保留每行source_id和包内原件/回执。原件中其他品种仅作为完整原始表留存，投影价格和products/current_contracts仅RB、HC。

v2顶层schema为user-data-gates-v2。products[].gates仍为backtest与daily_signal；supplementary和simulation_assumptions在产品层另列。实际手数null保持未知，任务的模拟数量由消费者显式配置，不能改写成实际手数。

通过标准：闭合路径和SHA、原件参数逐约复算、2021至2026逐年有效OHLC、目标日结算与日更证明、两品种范围、v1/v2互斥；正反控制至少覆盖参数日期/比例不符、原件篡改、历史缺年、真实最低开仓空值不阻断。候选须经本地验证，再与已授权的本地引擎会话对齐；资料包和实际引擎验收分开报告。

## 实际生成与核验

候选目录C:/Temp/tickflow-rb-hc-backtest-v2-20261117-v1，manifest SHA256为47d988f619fa978736d2ae7774fd39964ab28698d4479de657e6b9946e8876e1。共327件artifact，RB15945行/82个历史具体合约，HC15944行/81个历史具体合约；当前参数范围各12约、共24约。历史逐年有效日期分别为2021年243、2022至2024各242、2025年243、2026年209，实际观察不等于挂牌全集无缺日，也不表示24个当前约都有同样长的历史。

HC原件新增72份既有CSV及回执；1条目标日空OHLC未进入HC回测价格，仍原样保留在目标日日线和原始表。没有close补结算，没有量仓缺失补零。两条历史序列全部31889行已用独立Decimal/时间转换逐行对回包内原始CSV或16日上期所原表，327件SHA、身份、逐年日期、真实HC最低开仓null及两硬项范围核验通过；[独立价格复算](evidence/v2-backtest-control-readback-20261117.json)。

私有归档C:/VScodeProject/futures-tickflow-data/delivery/20261117/two-gates-v2/rb-hc-control-v1.zip，仅328个声明文件（含manifest），无空目录或缓存。SHA256为3fa29607497bf2e3e6bfaf56d391a162748f9a7b1d3230c6c85a1e7f5dbbee34，1956767字节，成员集及逐件迁移字节核同；[归档摘要](evidence/v2-backtest-control-archive-20261117.json)。此处尚未记作引擎执行验收通过。

## 本地回归与独立资料评估

代码候选e4ad3a6df7f70f8c7e17107a3dfa8ddfc01172f1已在独立干净副本通过三个Go模块的格式、vet、test，18个目录共269项Python测试，生成器还原前后指纹相同，doccheck203/893声明一致。日志C:/Temp/tickflow-v2-control-full-20261117.log，线上CI跳过、未打tag。反例包含源字节/路径、保证金原件比例/日期/单位、历史缺年和目标日空OHLC；实际minimum=null的HC正向通过。

另已实际读回本地引擎对81商品资料评估的7份证据（含manifest），逐件长度和SHA核同，Windows/Docker资料摘要一致，23项测试和6项实际拒绝控制记录通过；本仓没有重跑Docker。[本仓读回摘要](evidence/v2-assessment-consumer-readback-20261117.json)保留边界：只验证资料评估，不据此声称此价格包已在引擎执行，任务新增0、生产未改动。价格包的独立消费另待实际结果。
