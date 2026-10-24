# 非交易日六项收口

实施顺序：执行单生成与预检 → 假服务采集复核演练 → 异常退出 → 证据封存迁移 → 文档边界 → Windows/Linux持续集成及历史分支审计。

本批不联网取行情、不启动过期窗口、不发布版本。真实模式使用干净Git提交编译测试二进制，绑定代码SHA及二进制哈希，并调用该二进制的离线预检入口；计划指定明确授权截止时刻，不再把10/23今日17点当成所有后续任务的截止。未给新截止时刻时仍保留原同日17点保护。预检通过不代表交易日公告或行情连通性已验证。

合成模式必须明确标识synthetic，使用本地协议假服务和虚拟交易时钟；独立复核默认拒绝合成材料，只有显式允许合成演练时才分析，报告必须保留分类。成功退出、缺根、改价和无成功结束日志分别验证，不能以合成结果关闭真实验收。

执行单生成目录与采集任务目录分离，任务启动前校验编译二进制哈希，复制到新任务目录并落manifest；过期、SHA/参数/二进制不一致、输出目录已存在均拒绝。证据封存记录相对路径、大小和SHA256，拒绝链接越界与采集中封存；迁移后离线复核全部文件，再核对行情或合成分类。哈希清单只证明与封存时一致，不是外部可信签名。

异常退出以真实本地子进程强杀演练补齐；状态缺失、JSON损坏、未来心跳均报告未知，不能把错误当运行中。清理只针对演练自己创建的进程，不操作原始采集或其它程序。

持续集成运行现有模块枚举链及合成执行单闭环，不加载行情凭证。远端运行结果只有实际完成后才报告通过。历史分支按祖先、独有提交和相对主线改动分类，只生成处置清单，不删除或自动合并历史分支。

## 工具入口与边界

真实执行单由INS、DAYS、START、BEGIN、DROP、RESUME、END、DEADLINE八个字符串字段构成；DEADLINE是本次明确授权的截止，不是绕过旧窗口的开关。准备和启动都会调用同一编译产物的离线预检，必须实际执行指定测试且通过，零测试或跳过不通过。

```text
python tools/probe/capture_task/prepare.py C:\Temp\new-preparation --mode real --spec C:\Temp\approved-spec.json
python tools/probe/capture_task/cli.py launch C:\Temp\new-task --plan C:\Temp\new-preparation\plan.json
python tools/probe/capture_task/cli.py status C:\Temp\new-task
python tools/audit/evidence_bundle.py seal C:\Temp\new-task
python tools/audit/evidence_bundle.py verify C:\Temp\copied-task
```

合成模式不接受真实窗口配置，可一条命令走完整闭环：

```text
python tools/probe/capture_task/drill.py C:\Temp\new-offline-drill
```

该演练确实调用本地协议假服务上的Live，关闭旧连接、恢复并请求历史；原始参照值和接收时刻由假服务模型及虚拟时钟构造，不是另一条真实市场参照流，也不测32分钟真实墙钟断网。独立复核对48根和31根完整虚拟分钟作结构校验，并拒绝缺根、改价、无结束日志；默认入口拒绝带synthetic标识的材料。迁移复核保留synthetic分类，不能因目录复制改成real。

证据封存包含manifest、计划、编译产物、数据、日志与状态的相对路径/大小/哈希。未结束禁止封存；失败任务须显式--incomplete，保留未完成分类，不能转成通过。清单不是外部签名，不能防止攻击者同时重写整个目录和清单。

实际进程强杀演练只针对测试亲自创建的PID，缺失/损坏状态和未来心跳均报告supervisor_unknown。仍不提供任意派生进程树的强制清理或系统重启后自动恢复。

CI矩阵为Linux Go1.23/1.26和Windows Go1.26，Python3.12；禁用自动Go工具链升级以核验最低版本。Linux Go1.26另做三个模块竞态检查。Actions引用按官方仓库的v6标签核验后固定SHA，配置依据见 [checkout](https://github.com/actions/checkout)、[setup-go](https://github.com/actions/setup-go)、[setup-python](https://github.com/actions/setup-python)。工作流配置完成不等于远端检查通过，最终以实际运行记录为准。

当前adapter已在主线，契约对应状态已同步；仍未发布v0.11。历史设计中的旧日期保留为历史记录，当前实现和验收状态以当天进度及本文件为准。历史分支处置见 [branch-audit-20261024.md](branch-audit-20261024.md)。

## Windows CI 首轮失败与修正

候选dd4761c的本地三个Go模块及45项Python测试、还原演练通过；首轮远端Linux两个版本通过，含Go1.26三模块竞态检查，但Windows两个独立启动测试因宿主Job不允许CREATE_BREAKAWAY_FROM_JOB而返回WinError5，故整轮失败，不以Linux通过代替Windows。

默认process_scope=independent保留脱离父作业的要求，失败不自动降级；真实采集产物只允许independent。新增显式job作用域供合成CI演练使用，Windows该模式不设置breakaway，允许启动命令退出后继续工作，但随宿主Job结束而终止。Linux对应不新建session。计划、manifest、启动回执和状态都记录作用域；CI明确选job，不能把此结果宣称为已验证脱离宿主作业的持久采集。两种作用域的标志、禁止静默降级和真实模式拒绝job均有测试。
