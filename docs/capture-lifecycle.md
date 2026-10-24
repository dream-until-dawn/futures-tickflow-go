# 采集任务生命周期与活性监测

实施前边界：10/23 原进程停止原因未知；本方案降低后续任务依赖会话句柄的风险，不将“独立启动”描述为能抵抗系统关机、权限回收或进程强杀。先离线验证，不重新启动已过期的行情窗口。

实现放在 tools/probe/capture_task，分为执行计划、监护进程和命令入口。launch 使用实际 Python 解释器启动独立监护进程，标准输入关闭，输出写文件；Windows 使用新进程组、无窗口、脱离控制台及父作业标志。若宿主不允许脱离父作业，启动直接失败，不降级为可能随会话退出的后台任务。监护进程直接管理采集子进程，采集命令必须是前台程序，不能自行派生后台任务后退出。生产建议先编译 Go 测试二进制，再直接执行，避免 go run/go test 的多级进程。

计划显式指定 command 参数数组、cwd、watch 相对路径、有限 max_seconds、startup_seconds、idle_seconds、poll_seconds。新任务目录独占创建，参数中的 {task_dir} 替换为该绝对路径。只在新目录内监测指定文件，不扫描历史目录，不把日志或监护状态自身当成行情活性。命令参数不得带凭证；凭证由启动环境继承，工具不导出环境。启动等待和计划断开期间应配置合理阈值；活性告警不自动断言市场异常。

10/24补充：非敏感env参数绑定、逐通道quiet_windows与run.log接入见 [capture-phase-plan.md](capture-phase-plan.md)。计划暂停必须单独声明，不通过增大所有通道的空闲阈值掩盖故障。

状态区分 waiting、running、stalled、completed、failed、timed_out；监护心跳与文件最后变化分别记录。stalled 可以恢复，保留转换事件和次数；子进程正常退出但从未观察到数据时必须失败。completed 仅代表进程退出零且有文件活动，行情验收仍需独立复核。status 对超期心跳报告 supervisor_unknown，绝不凭旧状态宣称仍在采集。超时终止直接子进程并记录实际退出码；不声称任意派生进程树均已清理。

每个 watch 文件独立判定；一条通道持续更新不能掩盖另一条缺失或停写。watch 只列需要持续更新的录制文件，不能把只在结束时生成的历史汇总当成连续活性通道。监测采用大小与纳秒修改时间，不解析内容，文件变化不证明行情有效。Windows 状态文件读取/替换出现短时共享冲突时有限重试，持续失败则不报成功。

验证先覆盖正常写入、无数据成功退出、非零退出、启动失败、暂停后恢复、永远无输出、总时限、重复目录、路径越界、非法阈值、过期监护心跳；再用独立启动器退出后仍持续写入的离线子进程证明脱离调用方能力。所有文件留在临时目录，测试不得启动真实网络采集或覆盖10/23原始材料。

## 使用方式

执行计划示例（路径替换为本机已编译的前台采集程序）：

```json
{
  "command": ["C:\\capture\\recorder.exe", "--out", "{task_dir}\\data"],
  "cwd": "C:\\capture",
  "watch": ["data/reference.jsonl", "data/live.jsonl"],
  "max_seconds": 3600,
  "startup_seconds": 300,
  "idle_seconds": 180,
  "poll_seconds": 1
}
```

```text
python tools/probe/capture_task/cli.py launch C:\Temp\new-capture-task --plan C:\Temp\capture-plan.json
python tools/probe/capture_task/cli.py status C:\Temp\new-capture-task --stale-seconds 10
```

launch 返回 launched 只代表监护创建请求成功，随后必须读取 status 确认 child_pid、心跳及数据变化；错误、超期或 stalled 状态退出码为1。状态文件是本地持久告警，不自动向外发送消息。采集 stdout/stderr 为 capture.log，监护输出为 worker.log，状态转换为 events.jsonl。

现有长缺口运行器通过环境变量接收 OUT：启动前将 TICKFLOW_GAP_OUT 指向新任务目录下的 capture 子目录，watch 列实际连续写入文件。运行前仍需按 long-gap-runner.md 生成新窗口执行单、编译测试二进制并设定 opt-in；本监护器不替代这些业务预检，不重置过期保护。独立复核所需 manifest/run.log 应由该执行单明确保存；不能把监护 completed 当作复核 PASS。

## 开发期验证记录

第一版 Windows 仅脱离控制台未验成：启动器退出后监护未完成；启用脱离父作业后又由实测检出状态文件替换 PermissionError。补齐有限共享冲突重试后，13项生命周期测试全部通过，包括启动器已退出后至少两次不同文件内容及最终内容15的检查。初次沙箱临时目录的访问拒绝属于环境失败，不计业务反例通过。

跨工具调用的保留探针目录 C:\Temp\tickflow-lifecycle-proof-1792756084380：首次启动命令已退出，随后独立调用读取到 running 状态和数据21–23；最终 completed、子进程退出码0、30行数据0–29，事件顺序 waiting → running → completed。原始数据 SHA256 为 aa2a825a4b21d8a3a98535ec490c903c34af63e22c6b6860d9b691463f495864。此为本地合成写入，不是真实行情。

5e8dee7 的首次全仓检查发现中文 Python 输出守卫未通过：reconfigure 不符合仓库规定的双流包裹形式。已改为只在直接入口包裹 stdout/stderr，模块导入不改调用方输出流；未放宽守卫。该轮其它模块及24项Python测试、还原演练通过，但总结果为失败，不计全绿。

基线5e8dee7的仓库外突变复核位于 C:\Temp\tickflow-lifecycle-mutations-79040de8d8ed4fd988c0e9a8596e6c2d：零数据退出仍成功、忽略过期心跳、忽略停写三项全部 KILLED，均为目标断言失败，非导入/编译失败；基线与还原后均退出0。被测 worker.py SHA256 为301a1f04170fcdf49fdacd6df8c279cf45c262c01b393d854d66e80527d9bd1e。

修复后的固定提交 f04124a 运行 module_sweep.py：三个Go模块 gofmt/vet/test全部通过，Python三个目录共24项全部通过，生成器还原演练通过、前后内容指纹均为e3b0c44298fc；运行前后工作区干净。至此生命周期离线验收通过，不改变真实行情验收状态。

补充长期打开写入句柄的探针 C:\Temp\tickflow-open-writer-fkg5geyt：同一个打开文件分12次写入并flush，未依赖反复关闭文件；监护识别 waiting → running → completed，停写次数0、子进程退出0。它只验证本机该写入方式的文件活性可观察性，不推断其它文件系统或未flush缓冲的可见性。
