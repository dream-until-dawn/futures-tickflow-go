package batch

import (
	"context"
	"fmt"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
)

// SyncResult 区分过程Complete和请求覆盖；原有SyncReport.Complete只回答过程是否有异常。
type SyncResult struct {
	Request tf.SyncRequest `json:"request"`
	Report  tf.SyncReport  `json:"report"`
	Covered bool           `json:"covered"`
}

// SyncFrozen 在改库前核验完整日历区间，并在同步后验证每个预期交易日确有Bar。
// 不把Syncer裁剪区间后的过程成功当成全请求覆盖；调用方仍需独立生命周期/挂牌清单。
func SyncFrozen(ctx context.Context, cfg tf.SyncerConfig, request tf.SyncRequest, now int64) (SyncResult, error) {
	result := SyncResult{Request: request}
	if cfg.Calendar == nil || cfg.Store == nil || request.To == 0 || request.Period != tf.Daily {
		return result, fmt.Errorf("batch: 冻结同步须日历/存储及明确末日")
	}
	var expected []tf.TradingDay
	if err := cfg.Calendar.Walk(request.Symbol.ProductKey(), request.From, request.To, func(d tf.Day) bool { expected = append(expected, d.Num); return true }); err != nil {
		return result, err
	}
	syncer, err := tf.NewSyncer(cfg)
	if err != nil {
		return result, err
	}
	result.Report, err = syncer.Sync(ctx, request, now)
	if err != nil {
		return result, err
	}
	if !result.Report.Complete() {
		return result, fmt.Errorf("batch: 同步过程不完整")
	}
	got, err := cfg.Store.DaysWithBars(tf.Span{From: request.From, To: request.To})
	if err != nil {
		return result, err
	}
	// DaysWithBars返回实际有记录的交易日集合，既有coverage不能替代数据存在。
	if len(got) != len(expected) {
		return result, fmt.Errorf("batch: 请求覆盖不足，实际%d预期%d个交易日", len(got), len(expected))
	}
	for _, d := range expected {
		if !got[d] {
			return result, fmt.Errorf("batch: 交易日集合不符")
		}
	}
	result.Covered = true
	return result, nil
}
