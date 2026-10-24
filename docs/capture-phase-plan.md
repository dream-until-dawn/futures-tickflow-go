# 采集执行计划与预期暂停

2026-10-24实施前设计：主线0351b3b已有独立监护；但长缺口试验故意暂停Live超过31分钟，统一idle_seconds不能同时兼顾参照通道灵敏度和Live计划停写。另有真实运行参数仅依赖启动时环境、stdout日志名不匹配独立复核器run.log的问题。

本批只做离线接入：计划可固定白名单内的非敏感环境参数，支持明确stdout文件名和逐通道绝对时间暂停窗口。窗口必须带时区、有限、起止严格递增、不重叠且归属于watch通道；只在该窗口抑制该通道停写告警，不抑制监护心跳、不放宽参照、总超时、进程非零退出或结束时缺文件的判据。恢复后的首根可等一个idle_seconds宽限，期间明确列为quiet_files，不能称数据持续活跃。

凭证继续只继承父进程环境，不写入计划。env仅允许TICKFLOW_GAP_INS、DAYS、START、BEGIN、DROP、RESUME、END、OUT、REAL及TICKFLOW_LIVE；未知键、凭证键、空值和非字符串拒绝。路径占位符仅在OUT和command参数替换；OUT在执行前解析并约束为新任务目录的子目录，拒绝借路径写回历史资料。stdout文件仅允许capture.log或run.log，均不允许成为watch活性来源。

验证包括计划暂停内零误报、同一时刻参照缺失仍告警、窗口结束仍无数据必须告警、窗口不使非零退出或缺文件结束通过、非敏感参数固定且凭证不复制、越界OUT与非法窗口拒绝，以及独立启动入口保存plan、环境参数和run.log闭环。不会启动10/23过期窗口，不产生真实行情验收结论。

## 长缺口执行单接入方式

沿用 [capture-lifecycle.md](capture-lifecycle.md) 的launch/status入口，command改为新编译的Go测试二进制，参数为 -test.run=^TestLiveLongGapReal$、-test.v、-test.timeout=0，cwd固定为编译该二进制的仓库source/shinnysource目录。执行计划新增以下字段，所有占位符须替换后才可验证，不提供过期窗口的可直接启动命令：

```json
{
  "stdout_name": "run.log",
  "env": {
    "TICKFLOW_GAP_INS": "<固定合约>",
    "TICKFLOW_GAP_DAYS": "<已核实的交易日列表>",
    "TICKFLOW_GAP_START": "<首根RFC3339>",
    "TICKFLOW_GAP_BEGIN": "<未来起跑RFC3339>",
    "TICKFLOW_GAP_DROP": "<主动断开RFC3339>",
    "TICKFLOW_GAP_RESUME": "<恢复RFC3339>",
    "TICKFLOW_GAP_END": "<结束右开边界RFC3339>",
    "TICKFLOW_GAP_OUT": "{task_dir}/capture",
    "TICKFLOW_GAP_REAL": "1",
    "TICKFLOW_LIVE": "1"
  },
  "watch": ["capture/reference.jsonl", "capture/live.jsonl"],
  "quiet_windows": {
    "capture/live.jsonl": [
      {"start": "<DROP RFC3339>", "end": "<RESUME加首根宽限RFC3339>"}
    ]
  }
}
```

startup_seconds须覆盖等待BEGIN到首根的时间；idle_seconds按参照接收与正常分钟交付频率选择；max_seconds覆盖末根观察、最终历史请求和校验的有限余量。quiet窗口只配置Live，不给reference统一豁免。原运行器仍核验未来窗口、连续交易时段、至少31根完整分钟与明确opt-in；本扩展不绕过其预检。

launch后仍须核验child_pid、心跳和各通道变化。stdout_name仅对齐复核日志名，manifest.json的合约、时刻、代码SHA及编译二进制哈希仍需执行准备阶段明确封存；没有执行单或成功结束日志时，独立复核器继续拒绝。quiet_files只说明计划允许停写，不是行情有效性声明。
