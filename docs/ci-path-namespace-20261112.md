# Windows监护路径误判修复

本轮全仓验证中，三个Go模块通过，合成脱离启动任务在observe阶段ValueError失败。没有放宽时限或删测试。原始完整失败日志位于`C:/Temp/tickflow-session-followup-20261112/module-sweep.log`。

仓库外仪表化副本20轮重放中第12、15轮复现：任务根为普通`C:\Temp\...\task-12`，候选`Path.resolve()`返回`\\?\C:\Temp\...\task-12\capture\raw.jsonl`。两者是同一文件系统子目录，但旧字面父子比较判为越界。诊断分别保留`capture-diagnostic/task-12/path-error.json`和第15轮同名文件，没有打印继承凭证或命令环境。

内部`tools/probe/capture_task/paths.py`统一已resolve的合法绝对盘符与UNC的扩展文件命名空间后比较；保留符号链接解析，不简单剥除设备前缀。监护和OUT检查复用该函数，跨目录/盘符、同名路径前缀、未知设备命名空间和未消解父目录继续拒绝，OUT仍必须是严格子目录。原模拟越界测试分别保留候选越界和原根两次resolve的独立结果，拒绝断言仍在。

实际修复代码副本20轮全部完成，42项监护正反测试通过，固定回归覆盖记录中的普通/扩展盘符、UNC、大小写、越界、设备路径及严格子目录。随后全仓三个Go模块gofmt/vet/test、94项Python、还原演练、doccheck和三个模块串行race全部通过。第一次失败与前后对照均保留，不声称首次全绿或穷尽Windows全部路径竞争。

本次仅合成任务验证，没有现场连接或采集，真实任务independent门禁保持；未修改资金政策，未打tag。
