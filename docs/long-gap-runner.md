# 长缺口真实行情运行器设计

2026-10-23，使用者授权持续推进至东八区今日 17:00 前。实现先复用已标定的 Live/drop 控制及 L8 原始帧格式，但不调用固定九月日期的旧入口。

配置全部显式传入：具体合约、交易日列表、起始根、开始、断开、恢复、结束时刻、仓库外新目录。只有 TICKFLOW_GAP_REAL=1 才运行；启用后参数错误必须失败。窗口必须仍在未来，同属日历的一段连续交易时段，分钟对齐，恢复与断开至少间隔 31 个完整分钟，结束不超过当日 17:00。缺凭证不算完成。

甲为持续原始 websocket 参照，不主动重连；甲断开即无结论。乙为同一 Live 实例，断开点通过 Next 上下文期限回到调用方，排空已交付队列后 drop，关闭旧连接，等待恢复点再继续 Next；维持默认冻结和重连参数。每次调用均有界，结束后关闭两路。原始帧、Live 输出、控制事件和最终历史根使用新目录独占创建，绝不覆盖原证据。

结束后取历史根，与参照和 Live 分别逐根核对 O/H/L/C/V/OI、标签、交易日及完整区间；缺口内参照须实际覆盖至少 31 根已完结根。Live 全区间不重不漏，必须有断开前和恢复后输出，推送恰好连接两次，计数显示发生额外历史请求。任何缺帧、通道不一致、时钟或连接异常均保留数据并报告失败/无结论；不以退出码零或日志存在代替结论。

离线验证配置拒绝：过期、时序错误、跨休市、短缺口、无效日历、非分钟边界，以及完整性分析的缺根、改值、重复和缺少恢复后输出。生产 Live 不新增 API；运行器只存在于测试侧。

## 2026-10-23 下午执行单

使用者已授权持续推进至今日 17:00。固定合约 SHFE.rb2701，日历 20261022、20261023；起始根 13:30，BEGIN 13:31，DROP 13:36，RESUME 14:08，END 14:18（东八区）。END 是历史右开边界，原始录制与 Live 继续至 14:18:10，为末根确认留十秒。原始资料落在系统临时目录的新子目录中，不进 Git。

时段核验来源：[上期所交易时间](https://www.shfe.com.cn/services/calenderandholidays/tradinghours/)、[2026 休市安排](https://www.shfe.com.cn/services/calenderandholidays/holiday/)。本次只采用日盘 13:30–15:00，不据搜索结果推断夜盘时段。

只读预检已取回本合约今日 11:25–11:29 五根历史行情，连续完整；公开主连映射五秒超时，只作无结论，不声称本合约是当前主力。预检是在开发树上运行的连通性读数，正式窗口使用已提交代码。

819819d 的干净独立克隆三模块 gofmt/vet/test、生成器还原演练与 doccheck 通过；前后 git status --porcelain 均为 ""，还原指纹 e3b0c44298fc。新增离线校验含七项参数反例及八项完整性反例，均能拒绝；既有四项长缺口控制继续通过。竞态检查未运行成功：Windows GCC 因中文安装路径找不到 crt2.o/crtbegin.o，记为工具链阻断，不能记成通过。

实跑环境变量统一为 TICKFLOW_GAP_ 加 INS、DAYS、START、BEGIN、DROP、RESUME、END、OUT；启用 TICKFLOW_GAP_REAL=1 与 TICKFLOW_LIVE=1。命令为 go test ./source/shinnysource -run '^TestLiveLongGapReal$' -count=1 -v -timeout 0。只读连通性另用 TICKFLOW_GAP_CHECK=1，筛选 '^TestGapMarketPreflight$'。

录制进程已启动等待 BEGIN，代码基线 24b0f0d；证据根目录 C:\Temp\tickflow-gap-20261023-07943ea3fc174fad872bfacd9e0c4f90，含 manifest.json、run.log 及 capture 子目录。此状态不等于取数成功，必须待窗口结束审阅原始结果。

路径 A 另由 TestLiveDefaultRetryExhaustion 校验：默认参数下三次握手失败，返回 ErrDisconnected；服务恢复后继续 Next 仍返回同一错误，不能把已终止实例当作可恢复实例。该离线测试通过，不外推真实服务持续不可达表现。
