package shinnyref

// 以下别名保持原错误身份不变，供包外errors.Is分类；契约见docs/contract.md。
var (
	// ErrNotFuture 表示该记录不是期货，不能作为未知/空目录吞掉。
	ErrNotFuture = errNotFuture
	// ErrMissing 表示必须字段缺失，调用方不得补造。
	ErrMissing = errMissing
	// ErrBadOffset 表示时段端点解析或范围无效。
	ErrBadOffset = errBadOffset
	// ErrBadSessions 表示时段结构不受支持。
	ErrBadSessions = errBadSessions
	// ErrNoGzip 表示未按完整压缩下载契约返回。
	ErrNoGzip = errNoGzip
	// ErrNotReadToEOF 表示调用方未读完，完整性尚未经验证。
	ErrNotReadToEOF = errNotReadToEOF
	// ErrStreamBroken 表示流完整性检查失败，不能使用本次目录。
	ErrStreamBroken = errStreamBroken
	// ErrBadStatus 表示非200响应；206另归ErrRangeAsked。
	ErrBadStatus = errBadStatus
	// ErrRangeAsked 表示206响应，不接受Range拼接快照。
	ErrRangeAsked = errRangeAsked
)
