package service

import (
	"os"
	"sync"
	"testing"

	"github.com/new-api-tools/backend/internal/config"
)

// resetHistorySingleton clears the lazily-initialized singleton so each test
// can open a fresh database under its own temp dir.
func resetHistorySingleton() {
	historyOnce = sync.Once{}
	historyInst = nil
	historyErr = nil
}

func TestModelHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("DATA_DIR", dir)
	os.Setenv("SQL_DSN", "user:pass@tcp(localhost:3306)/db")
	defer os.Unsetenv("DATA_DIR")
	defer os.Unsetenv("SQL_DSN")
	config.Load()
	resetHistorySingleton()

	hist, err := GetModelHistoryService()
	if err != nil {
		t.Fatalf("GetModelHistoryService failed: %v", err)
	}
	defer func() {
		hist.Close()
		resetHistorySingleton()
	}()

	const date = "2026-06-08"
	start := dayStartTimestamp(date)
	if start == 0 {
		t.Fatalf("dayStartTimestamp returned 0 for %s", date)
	}

	snap := &daySnapshot{
		date:    date,
		startTS: start,
		models: map[string]*dailyPerfStats{
			"gpt-4": {
				totalRequests:       10,
				successCount:        8,
				failureCount:        1,
				formatError:         1,
				emptyCount:          1,
				timedRequests:       8,
				within5s:            6,
				within10s:           7,
				outputRequests:      8,
				completionTokensSum: 800,
				useTimeSum:          400,
			},
		},
		slots: map[string]map[int]*slotCounts{
			"gpt-4": {
				0: {
					total:               5,
					success:             4,
					failure:             1,
					formatError:         1,
					empty:               0,
					timedRequests:       4,
					within5s:            3,
					within10s:           4,
					outputRequests:      4,
					cacheDenominatorSum: 100,
					cacheTokensSum:      40,
					cacheWriteTokensSum: 10,
					inputTokensSum:      120,
					outputTokensSum:     200,
					completionTokensSum: 200,
					useTimeSum:          100,
				},
				13: {total: 5, success: 4, failure: 0, empty: 1},
			},
		},
		channels: map[int64]*dailyPerfStats{
			7: {
				totalRequests:       9,
				successCount:        7,
				failureCount:        1,
				formatError:         1,
				emptyCount:          1,
				timedRequests:       9,
				within5s:            5,
				outputRequests:      9,
				completionTokensSum: 900,
				useTimeSum:          300,
			},
		},
		chanSlot: map[int64]map[int]*slotCounts{
			7: {
				0: {
					total:                 4,
					success:               3,
					failure:               1,
					formatError:           1,
					empty:                 0,
					timedRequests:         3,
					within5s:              2,
					durationTimedRequests: 3,
					durationWithin10s:     2,
					outputRequests:        3,
					claudeRequests:        1,
					cacheDenominatorSum:   100,
					cacheTokensSum:        25,
					cacheWriteSum:         10,
					cacheWriteTokensSum:   10,
					inputTokensSum:        100,
					outputTokensSum:       150,
					completionTokensSum:   150,
					useTimeSum:            50,
				},
				13: {total: 5, success: 4, failure: 0, empty: 1},
			},
		},
		chanName: map[int64]string{7: "primary"},
	}

	if err := hist.SaveDay(snap); err != nil {
		t.Fatalf("SaveDay failed: %v", err)
	}

	// HasDate
	has, err := hist.HasDate(date)
	if err != nil || !has {
		t.Fatalf("HasDate=%v err=%v, want true", has, err)
	}
	if has, _ := hist.HasDate("2020-01-01"); has {
		t.Fatalf("HasDate for empty date returned true")
	}

	// ListAvailableDates
	dates, err := hist.ListAvailableDates()
	if err != nil || len(dates) != 1 || dates[0] != date {
		t.Fatalf("ListAvailableDates=%v err=%v", dates, err)
	}

	// GetAvailableModelsByDate
	models, err := hist.GetAvailableModelsByDate(date)
	if err != nil || len(models) != 1 {
		t.Fatalf("GetAvailableModelsByDate=%v err=%v", models, err)
	}
	if models[0]["model_name"] != "gpt-4" {
		t.Fatalf("unexpected model: %v", models[0])
	}

	// GetMultipleModelsStatusByDate — existing + non-existing model
	statuses, err := hist.GetMultipleModelsStatusByDate([]string{"gpt-4", "ghost"}, date)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("GetMultipleModelsStatusByDate len=%d err=%v", len(statuses), err)
	}
	g := statuses[0]
	if g["total_requests"].(int64) != 10 || g["success_count"].(int64) != 8 {
		t.Fatalf("gpt-4 summary wrong: %v", g)
	}
	if g["success_rate"].(float64) != 80.0 {
		t.Fatalf("expected success_rate 80, got %v", g["success_rate"])
	}
	if g["format_error_count"].(int64) != 1 || g["model_failure_count"].(int64) != 0 {
		t.Fatalf("gpt-4 classified failure counts wrong: %v", g)
	}
	slotData, ok := g["slot_data"].([]map[string]interface{})
	if !ok || len(slotData) != historySlotCount {
		t.Fatalf("slot_data len wrong: %d", len(slotData))
	}
	// slot 0 start_time must equal day start
	if slotData[0]["start_time"].(int64) != start {
		t.Fatalf("slot0 start_time=%v want %d", slotData[0]["start_time"], start)
	}
	if slotData[0]["total_requests"].(int64) != 5 {
		t.Fatalf("slot0 total wrong: %v", slotData[0]["total_requests"])
	}
	if slotData[0]["format_error_count"].(int64) != 1 || slotData[0]["model_failure_count"].(int64) != 0 {
		t.Fatalf("slot0 classified failure counts wrong: %v", slotData[0])
	}
	if slotData[0]["cache_hit_rate"] != 40.0 || slotData[0]["completion_tps"] != 2.0 {
		t.Fatalf("slot0 performance wrong: %v", slotData[0])
	}
	if slotData[0]["cache_hit_tokens"].(int64) != 40 || slotData[0]["cache_write_tokens"].(int64) != 10 || slotData[0]["total_input_tokens"].(int64) != 120 || slotData[0]["total_output_tokens"].(int64) != 200 {
		t.Fatalf("slot0 token counts wrong: %v", slotData[0])
	}
	// ghost model -> zero filled
	ghost := statuses[1]
	if ghost["total_requests"].(int64) != 0 {
		t.Fatalf("ghost should be zero-filled, got %v", ghost["total_requests"])
	}
	if len(ghost["slot_data"].([]map[string]interface{})) != historySlotCount {
		t.Fatalf("ghost slot_data should still have %d slots", historySlotCount)
	}

	// GetChannelPerformanceByDate
	chans, err := hist.GetChannelPerformanceByDate(date)
	if err != nil || len(chans) != 1 {
		t.Fatalf("GetChannelPerformanceByDate=%v err=%v", chans, err)
	}
	if chans[0]["channel_name"] != "primary" {
		t.Fatalf("channel name wrong: %v", chans[0])
	}
	if chans[0]["total_requests"].(int64) != 9 {
		t.Fatalf("channel total_requests wrong: %v", chans[0]["total_requests"])
	}
	if chans[0]["success_count"].(int64) != 7 || chans[0]["failure_count"].(int64) != 1 || chans[0]["empty_count"].(int64) != 1 {
		t.Fatalf("channel availability counts wrong: %v", chans[0])
	}
	if chans[0]["format_error_count"].(int64) != 1 || chans[0]["model_failure_count"].(int64) != 0 {
		t.Fatalf("channel classified failure counts wrong: %v", chans[0])
	}
	if chans[0]["success_rate"].(float64) != 77.78 {
		t.Fatalf("channel success_rate wrong: %v", chans[0]["success_rate"])
	}
	channelSlotData, ok := chans[0]["slot_data"].([]map[string]interface{})
	if !ok || len(channelSlotData) != historySlotCount {
		t.Fatalf("channel slot_data len wrong: %d", len(channelSlotData))
	}
	if channelSlotData[0]["total_requests"].(int64) != 4 || channelSlotData[0]["success_count"].(int64) != 3 || channelSlotData[0]["failure_count"].(int64) != 1 {
		t.Fatalf("channel slot0 wrong: %v", channelSlotData[0])
	}
	if channelSlotData[0]["format_error_count"].(int64) != 1 || channelSlotData[0]["model_failure_count"].(int64) != 0 {
		t.Fatalf("channel slot0 classified failure counts wrong: %v", channelSlotData[0])
	}
	if channelSlotData[0]["cache_hit_rate"] != 25.0 || channelSlotData[0]["cache_write_rate"] != 10.0 || channelSlotData[0]["completion_tps"] != 3.0 {
		t.Fatalf("channel slot0 performance wrong: %v", channelSlotData[0])
	}
	if channelSlotData[0]["cache_hit_tokens"].(int64) != 25 || channelSlotData[0]["cache_write_tokens"].(int64) != 10 || channelSlotData[0]["total_input_tokens"].(int64) != 100 || channelSlotData[0]["total_output_tokens"].(int64) != 150 {
		t.Fatalf("channel slot0 token counts wrong: %v", channelSlotData[0])
	}
	if channelSlotData[13]["total_requests"].(int64) != 5 || channelSlotData[13]["empty_count"].(int64) != 1 {
		t.Fatalf("channel slot13 wrong: %v", channelSlotData[13])
	}

	// Idempotent re-save: same date should replace, not duplicate.
	if err := hist.SaveDay(snap); err != nil {
		t.Fatalf("re-SaveDay failed: %v", err)
	}
	dates2, _ := hist.ListAvailableDates()
	if len(dates2) != 1 {
		t.Fatalf("re-save duplicated dates: %v", dates2)
	}
}

func TestParseCustomTimeWindow(t *testing.T) {
	tests := []struct {
		name       string
		window     string
		wantOK     bool
		wantTotal  int64
		normalized string
	}{
		{name: "preset", window: "15m", wantOK: true, wantTotal: 900, normalized: "15m"},
		{name: "minutes", window: "45min", wantOK: true, wantTotal: 2700, normalized: "45min"},
		{name: "legacy minute suffix", window: "45m", wantOK: true, wantTotal: 2700, normalized: "45min"},
		{name: "hours", window: "2h", wantOK: true, wantTotal: 7200, normalized: "2h"},
		{name: "seconds unsupported", window: "30s", wantOK: false},
		{name: "days unsupported", window: "1d", wantOK: false},
		{name: "zero unsupported", window: "0min", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, ok := ParseTimeWindow(tt.window)
			if ok != tt.wantOK {
				t.Fatalf("ParseTimeWindow(%q) ok=%v, want %v", tt.window, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cfg.totalSeconds != tt.wantTotal {
				t.Fatalf("totalSeconds=%d, want %d", cfg.totalSeconds, tt.wantTotal)
			}
			if got := NormalizeTimeWindow(tt.window); got != tt.normalized {
				t.Fatalf("NormalizeTimeWindow(%q)=%q, want %q", tt.window, got, tt.normalized)
			}
		})
	}
}
