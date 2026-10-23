# adapter 外部取用可行性探针

实施前方案：在仓库外创建全新的 Go 模块，关闭 GOWORK，按远端 main 的明确提交 9a2cabd 引入 github.com/dream-until-dawn/futures-tickflow-go/adapter。不得复制本机 go.mod 的 replace，不依赖兄弟仓或本地工作区源码。

编译并运行最小调用：正常1m根转换应保留合约、交易日、时间与 Close；NaN应拒绝且返回零结果。打印有效模块版本，检查无本地 replace。此探针只证明固定提交在外部模块的可解析性与最小转换行为，不证明发布 tag、真实持仓、撮合或完整 API 收口。

若远端解析、校验和或网络失败，按原始错误记录，不通过本机 replace 绕过。所有临时源文件与 go.mod/go.sum 放仓库外，保留可复查结果，不改变生产依赖版本。

## 10/23 探针结果

构建阶段完成 go mod init 与 go get 后，临时夹具独立提交为 f258ee17520ada4bf3868f31bd5e7659f6304afc，目录 C:\Temp\tickflow-external-consumer-0020739530974b04a1f284c8abfe88fb。随后在该干净树、GOWORK=off 下执行 go run . 与 go list -m all，前后 porcelain 均为 ""，正常转换及 NaN 拒绝均通过。正常格明确断言 SHFE/rb/2027/1、交易日、起止毫秒与 Close，错误格断言返回错误及完整零结果。

有效依赖无 replace：adapter v0.0.0-20261023044450-9a2cabdabea1，父行情模块 v0.10.0，模拟器 v0.0.0-20260923031436-6d3c7bbf3347，decimal v1.4.0。依赖模块中的本机 replace 未被继承，未靠本地兄弟仓或工作区代码编译。这证明当前固定远端提交可被独立消费者取用，仍未创建 adapter 发布 tag。

晚间收口再次在同一干净夹具运行 go run .、go list -m all，正常转换与NaN拒绝仍通过，模块版本无变化，工作区无差异。探针绑定的是上述明确远端提交，不把它描述为自动跟踪最新主线或新版依赖的验证。
