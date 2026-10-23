# 离线自检覆盖 Python 工具

本次工具新增 Python 预检和独立证据复核的测试，但现有 module_sweep 只枚举 Go 模块，未自动执行它们。统一入口继续保留 module_sweep.py，增加单独列出的 Python 检查阶段，不把它冒充第四个 Go 模块。

Python 阶段按 tools 下 test_*.py 所在目录枚举，每个目录用独立解释器进程发现并执行 unittest，避免同名模块污染。发现零测试、全部跳过或任一失败均返回非零。历史 rebuild_docs_test.py 不符合 test_*.py 命名，仍由已有还原演练单独负责。

先用隔离目录的通过、失败、零测试、全跳过四格校验退出码，再将其接入统一入口并在干净临时克隆跑整条链。该链保持纯离线，不设置任何真实行情启用环境变量，不运行实盘录制。

4fbe4c7 的独立干净克隆执行统一入口与 doccheck 全过：三个 Go 模块 gofmt/vet/test、tools/audit 五项及 tools/probe 六项 Python 测试、生成器还原演练。前后 git status --porcelain 均为 ""，还原指纹 e3b0c44298fc。目录发现另用新嵌套目录作正对照、node_modules 与旧式 rebuild_docs_test.py 作排除对照，不写死当前两个目录。
