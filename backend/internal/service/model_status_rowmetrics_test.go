package service

import (
	"encoding/json"
	"testing"
)

// mustOtherString 把 map 序列化为 logs.other 的字符串形态(模拟从 DB 读出的原始行)。
func mustOtherString(t *testing.T, other map[string]interface{}) string {
	t.Helper()
	if other == nil {
		return ""
	}
	raw, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal other failed: %v", err)
	}
	return string(raw)
}

// TestComputeRowMetricsGolden 用逐条手算的 golden 值锁定 computeRowMetrics 的派生逻辑。
// 这些期望值是在重构前由旧 accumulate / accumulatePerformanceRow 逐字段核对得到的基线,
// 删除旧函数后仍能防止 computeRowMetrics 回归。
func TestComputeRowMetricsGolden(t *testing.T) {
	rules := DefaultErrorRules()

	const startTime = int64(1_700_000_000)
	createdAt := startTime + 100

	cases := []struct {
		name  string
		row   map[string]interface{}
		other map[string]interface{}
		want  rowMetrics
	}{
		{
			name: "stream success with frt and cache",
			row: map[string]interface{}{
				"type": int64(2), "use_time": 8.0, "prompt_tokens": 100.0,
				"completion_tokens": 50.0, "is_stream": true,
			},
			other: map[string]interface{}{"frt": 3000.0, "cache_tokens": 20.0},
			// frt=3s → timed/w5s/w10s=1; useTime=8 → durTimed/durW10/durW20=1;
			// effectiveTime=max(8-3,1)=5; inputTokens=max(100,20)=100; denom=max(100,20)=100
			want: rowMetrics{
				valid: true, createdAt: createdAt, isSuccess: true, isPerf: true,
				timedRequests: 1, within5s: 1, within10s: 1,
				durationTimedRequests: 1, durationWithin10s: 1, durationWithin20s: 1,
				outputRequests: 1, completionTokensSum: 50, useTimeSum: 5,
				cacheTokensSum: 20, inputTokensSum: 100, outputTokensSum: 50,
				cacheDenominatorSum: 100,
			},
		},
		{
			name: "non-stream success",
			row: map[string]interface{}{
				"type": int64(2), "use_time": 12.0, "prompt_tokens": 200.0,
				"completion_tokens": 80.0, "is_stream": false,
			},
			other: map[string]interface{}{},
			// firstTokenTime=useTime=12 → timed=1,w5s=0,w10s=0; durTimed=1,durW10=0,durW20=1;
			// effectiveTime=12; inputTokens=max(200,0)=200; denom=200
			want: rowMetrics{
				valid: true, createdAt: createdAt, isSuccess: true, isPerf: true,
				timedRequests: 1, durationTimedRequests: 1, durationWithin20s: 1,
				outputRequests: 1, completionTokensSum: 80, useTimeSum: 12,
				inputTokensSum: 200, outputTokensSum: 80, cacheDenominatorSum: 200,
			},
		},
		{
			name: "claude messages request",
			row: map[string]interface{}{
				"type": int64(2), "use_time": 6.0, "prompt_tokens": 300.0,
				"completion_tokens": 120.0, "is_stream": true,
			},
			other: map[string]interface{}{
				"frt": 2000.0, "claude": true, "cache_tokens": 40.0, "cache_write_tokens": 15.0,
			},
			// frt=2s → timed/w5s/w10s=1; useTime=6 → durTimed/durW10/durW20=1;
			// effectiveTime=max(6-2,1)=4; claude → input=denom=300+40+15=355; cacheWriteSum=15; claudeReq=1
			want: rowMetrics{
				valid: true, createdAt: createdAt, isSuccess: true, isPerf: true,
				timedRequests: 1, within5s: 1, within10s: 1,
				durationTimedRequests: 1, durationWithin10s: 1, durationWithin20s: 1,
				outputRequests: 1, completionTokensSum: 120, useTimeSum: 4,
				cacheTokensSum: 40, cacheWriteTokensSum: 15, inputTokensSum: 355,
				outputTokensSum: 120, cacheDenominatorSum: 355, cacheWriteSum: 15, claudeRequests: 1,
			},
		},
		{
			name: "empty response (type=2 completion=0)",
			row: map[string]interface{}{
				"type": int64(2), "use_time": 4.0, "prompt_tokens": 10.0,
				"completion_tokens": 0.0, "is_stream": false,
			},
			other: map[string]interface{}{},
			// isEmpty + isPerf(type==2); firstTokenTime=4 → timed/w5s/w10s=1; durTimed/durW10/durW20=1;
			// completion=0 → 无 outputReq/outputTokens; inputTokens=max(10,0)=10; denom=10
			want: rowMetrics{
				valid: true, createdAt: createdAt, isEmpty: true, isPerf: true,
				timedRequests: 1, within5s: 1, within10s: 1,
				durationTimedRequests: 1, durationWithin10s: 1, durationWithin20s: 1,
				inputTokensSum: 10, cacheDenominatorSum: 10,
			},
		},
		{
			name: "failure rate limit (429)",
			row:  map[string]interface{}{"type": int64(5), "completion_tokens": 0.0},
			other: map[string]interface{}{"status_code": 429.0},
			want: rowMetrics{valid: true, createdAt: createdAt, isFailure: true, errorBucket: ErrorBucketRateLimit},
		},
		{
			name: "failure user format (400)",
			row:  map[string]interface{}{"type": int64(5), "completion_tokens": 0.0},
			other: map[string]interface{}{"status_code": 400.0},
			want: rowMetrics{valid: true, createdAt: createdAt, isFailure: true, errorBucket: ErrorBucketUserFormat},
		},
		{
			name: "failure model side (500)",
			row:  map[string]interface{}{"type": int64(5), "completion_tokens": 0.0},
			other: map[string]interface{}{"status_code": 500.0},
			// 500 → ErrorBucketModelSide,不计入 formatError/rateLimit
			want: rowMetrics{valid: true, createdAt: createdAt, isFailure: true, errorBucket: ErrorBucketModelSide},
		},
		{
			name:  "ignored log type",
			row:   map[string]interface{}{"type": int64(1)},
			other: map[string]interface{}{},
			want:  rowMetrics{}, // valid=false
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]interface{}{
				"created_at": createdAt,
				"other":      mustOtherString(t, tc.other),
			}
			for k, v := range tc.row {
				row[k] = v
			}

			got := computeRowMetrics(row, rules)
			if got != tc.want {
				t.Errorf("computeRowMetrics mismatch\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

// TestRowMetricsAccumulatorsConsistent 校验三个累加器把同一个 rowMetrics 折叠进各自
// 目标结构时字段一致,守护「实时模型/渠道/渠道模型 与 历史」共享同一份派生量的不变式。
func TestRowMetricsAccumulatorsConsistent(t *testing.T) {
	rules := DefaultErrorRules()
	const startTime = int64(1_700_000_000)
	const slotSeconds = int64(3600)
	const numSlots = 24

	row := map[string]interface{}{
		"type": int64(2), "created_at": startTime + 100, "use_time": 6.0,
		"prompt_tokens": 300.0, "completion_tokens": 120.0, "is_stream": true,
		"other": `{"frt":2000,"claude":true,"cache_tokens":40,"cache_write_tokens":15}`,
	}
	m := computeRowMetrics(row, rules)

	avail := newAvailabilityStats()
	avail.accumulateMetrics(m, startTime, slotSeconds, numSlots)
	if avail.totalRequests != 1 || avail.successCount != 1 {
		t.Fatalf("availability counters wrong: %+v", *avail)
	}
	slot := avail.slots[0]
	if slot == nil {
		t.Fatal("slot 0 not populated")
	}

	perf := &performanceStats{}
	accumulatePerformanceMetrics(perf, m)

	daily := &dailyPerfStats{}
	accumulateDailyMetrics(daily, m)

	// slot / performanceStats / dailyPerfStats 三者的性能字段应完全一致。
	if slot.useTimeSum != perf.useTimeSum || perf.useTimeSum != daily.useTimeSum {
		t.Errorf("useTimeSum diverged: slot=%v perf=%v daily=%v", slot.useTimeSum, perf.useTimeSum, daily.useTimeSum)
	}
	if slot.inputTokensSum != perf.inputTokensSum || perf.inputTokensSum != daily.inputTokensSum {
		t.Errorf("inputTokensSum diverged: slot=%v perf=%v daily=%v", slot.inputTokensSum, perf.inputTokensSum, daily.inputTokensSum)
	}
	if slot.cacheDenominatorSum != perf.cacheDenominatorSum || perf.cacheDenominatorSum != daily.cacheDenominatorSum {
		t.Errorf("cacheDenominatorSum diverged: slot=%v perf=%v daily=%v", slot.cacheDenominatorSum, perf.cacheDenominatorSum, daily.cacheDenominatorSum)
	}
	if perf.claudeRequests != 1 || daily.claudeRequests != 1 || slot.claudeRequests != 1 {
		t.Errorf("claudeRequests diverged: slot=%d perf=%d daily=%d", slot.claudeRequests, perf.claudeRequests, daily.claudeRequests)
	}
}
