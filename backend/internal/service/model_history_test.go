package service

import (
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/new-api-tools/backend/internal/config"
)

func TestChannelCostTrendsUseCompletedDaysAndPreservePreviousOnlyChannels(t *testing.T) {
	db, err := sql.Open("sqlite", "file:channel-cost-trends?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	hist := &ModelHistoryService{db: db}
	if err := hist.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	previousWeek := time.Now().AddDate(0, 0, -8).Format("2006-01-02")
	insert := `INSERT INTO model_daily_channel
		(date, channel_id, channel_name, total_requests, quota_sum)
		VALUES (?, ?, ?, ?, ?)`
	if _, err := db.Exec(insert, yesterday, 1, "current", 10, 1000); err != nil {
		t.Fatalf("insert current channel: %v", err)
	}
	if _, err := db.Exec(insert, previousWeek, 2, "previous-only", 5, 500); err != nil {
		t.Fatalf("insert previous-only channel: %v", err)
	}

	result, err := hist.GetChannelCostTrends(1, "week")
	if err != nil {
		t.Fatalf("GetChannelCostTrends: %v", err)
	}
	channels, ok := result["channels"].([]map[string]interface{})
	if !ok || len(channels) != 2 {
		t.Fatalf("channels=%T %v, want two channels", result["channels"], result["channels"])
	}

	var previousOnly map[string]interface{}
	for _, channel := range channels {
		current := channel["current"].([]map[string]interface{})
		if len(current) != 1 || current[0]["date"] != yesterday {
			t.Fatalf("current series must end yesterday: %v", current)
		}
		if toInt64(channel["channel_id"]) == 2 {
			previousOnly = channel
		}
	}
	if previousOnly == nil {
		t.Fatal("channel present only in the previous period was dropped")
	}
	current := previousOnly["current"].([]map[string]interface{})
	previous := previousOnly["previous"].([]map[string]interface{})
	if toInt64(current[0]["total_quota"]) != 0 || toInt64(previous[0]["total_quota"]) != 500 {
		t.Fatalf("previous-only channel series misaligned: current=%v previous=%v", current, previous)
	}
}

func TestMergeTodayChannelCostTrendsAddsCurrentWeekPoint(t *testing.T) {
	today := "2026-07-20"
	data := map[string]interface{}{
		"channels": []map[string]interface{}{
			{
				"channel_id":   int64(1),
				"channel_name": "existing-live",
				"current": []map[string]interface{}{
					{"date": "2026-07-19", "total_quota": int64(100)},
				},
			},
			{
				"channel_id":   int64(2),
				"channel_name": "existing-idle",
				"current": []map[string]interface{}{
					{"date": "2026-07-19", "total_quota": int64(50)},
				},
			},
		},
	}
	live := []map[string]interface{}{
		{
			"channel_id":          int64(1),
			"channel_name":        "existing-live",
			"total_quota":         float64(25),
			"total_input_tokens":  float64(300),
			"total_output_tokens": float64(40),
			"total_requests":      int64(7),
		},
		{
			"channel_id":          int64(3),
			"channel_name":        "live-only",
			"total_quota":         float64(10),
			"total_input_tokens":  float64(80),
			"total_output_tokens": float64(20),
			"total_requests":      int64(3),
		},
	}

	MergeTodayChannelCostTrends(data, live, today)

	channels, ok := data["channels"].([]map[string]interface{})
	if !ok || len(channels) != 3 {
		t.Fatalf("channels=%T %v, want three channels", data["channels"], data["channels"])
	}
	if data["includes_today"] != true {
		t.Fatalf("includes_today=%v, want true", data["includes_today"])
	}

	byID := make(map[int64]map[string]interface{}, len(channels))
	for _, channel := range channels {
		byID[toInt64(channel["channel_id"])] = channel
	}

	assertTodayPoint := func(channelID, quota, tokens, requests int64) {
		t.Helper()
		points := byID[channelID]["current"].([]map[string]interface{})
		var todayPoint map[string]interface{}
		for _, point := range points {
			if point["date"] == today {
				todayPoint = point
				break
			}
		}
		if todayPoint == nil {
			t.Fatalf("channel %d has no point for today: %v", channelID, points)
		}
		if toInt64(todayPoint["total_quota"]) != quota ||
			toInt64(todayPoint["total_tokens"]) != tokens ||
			toInt64(todayPoint["total_requests"]) != requests {
			t.Fatalf("channel %d today=%v, want quota=%d tokens=%d requests=%d", channelID, todayPoint, quota, tokens, requests)
		}
	}

	assertTodayPoint(1, 25, 340, 7)
	assertTodayPoint(2, 0, 0, 0)
	assertTodayPoint(3, 10, 100, 3)
}

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
		channelModels: map[int64]map[string]*dailyPerfStats{
			7: {
				"gpt-4": {
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
		},
		chanModelSlot: map[int64]map[string]map[int]*slotCounts{
			7: {
				"gpt-4": {
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
		},
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
	if chans[0]["model_count"].(int) != 1 {
		t.Fatalf("channel model_count wrong: %v", chans[0]["model_count"])
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

	detail, err := hist.GetChannelModelPerformanceByDate(date, 7, 100, 0)
	if err != nil {
		t.Fatalf("GetChannelModelPerformanceByDate failed: %v", err)
	}
	if detail["total"].(int) != 1 || detail["has_more"].(bool) {
		t.Fatalf("channel model detail paging wrong: %v", detail)
	}
	modelDetails := detail["data"].([]map[string]interface{})
	if len(modelDetails) != 1 || modelDetails[0]["model_name"] != "gpt-4" {
		t.Fatalf("channel model detail data wrong: %v", modelDetails)
	}
	if modelDetails[0]["channel_name"] != "primary" || modelDetails[0]["total_requests"].(int64) != 9 {
		t.Fatalf("channel model detail summary wrong: %v", modelDetails[0])
	}
	detailSlots := modelDetails[0]["slot_data"].([]map[string]interface{})
	if len(detailSlots) != historySlotCount || detailSlots[0]["total_requests"].(int64) != 4 {
		t.Fatalf("channel model detail slots wrong: %v", detailSlots)
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

func TestModelHistoryChannelModelDetailNotBuilt(t *testing.T) {
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

	const date = "2026-06-09"
	snap := &daySnapshot{
		date:    date,
		startTS: dayStartTimestamp(date),
		models: map[string]*dailyPerfStats{
			"gpt-4": {totalRequests: 1, successCount: 1},
		},
		slots: map[string]map[int]*slotCounts{
			"gpt-4": {0: {total: 1, success: 1}},
		},
		channels: map[int64]*dailyPerfStats{
			7: {totalRequests: 1, successCount: 1},
		},
		chanSlot: map[int64]map[int]*slotCounts{
			7: {0: {total: 1, success: 1}},
		},
		chanName: map[int64]string{7: "legacy"},
	}
	if err := hist.SaveDay(snap); err != nil {
		t.Fatalf("SaveDay failed: %v", err)
	}
	if _, err := hist.GetChannelModelPerformanceByDate(date, 7, 100, 0); !errors.Is(err, ErrHistoryChannelModelDetailNotBuilt) {
		t.Fatalf("expected ErrHistoryChannelModelDetailNotBuilt, got %v", err)
	}
}

func TestModelHistoryDailyTrendAggregationAndBackfillState(t *testing.T) {
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

	now := time.Now()
	completedDate := now.AddDate(0, 0, -2).Format("2006-01-02")
	missingQuotaDate := now.AddDate(0, 0, -3).Format("2006-01-02")
	today := now.Format("2006-01-02")

	completed := &daySnapshot{
		date:        completedDate,
		startTS:     dayStartTimestamp(completedDate),
		uniqueUsers: map[int64]struct{}{1: {}, 2: {}, 3: {}},
		models: map[string]*dailyPerfStats{
			"gpt-4": {
				totalRequests:       3,
				failureCount:        1,
				quotaSum:            12,
				inputTokensSum:      100,
				completionTokensSum: 40,
				cacheTokensSum:      20,
			},
			"claude": {
				totalRequests:       2,
				quotaSum:            8,
				inputTokensSum:      70,
				completionTokensSum: 30,
				cacheWriteTokensSum: 10,
			},
		},
		channels: map[int64]*dailyPerfStats{
			10: {
				inputTokensSum:      100,
				completionTokensSum: 40,
				cacheTokensSum:      20,
			},
			20: {
				inputTokensSum:      70,
				completionTokensSum: 30,
				cacheWriteTokensSum: 10,
			},
		},
		chanName: map[int64]string{10: "channel-a", 20: "channel-b"},
	}
	if err := hist.SaveDay(completed); err != nil {
		t.Fatalf("SaveDay completed failed: %v", err)
	}

	missingQuota := &daySnapshot{
		date:    missingQuotaDate,
		startTS: dayStartTimestamp(missingQuotaDate),
		models: map[string]*dailyPerfStats{
			"gpt-4":  {totalRequests: 4},
			"claude": {totalRequests: 6},
		},
	}
	if err := hist.SaveDay(missingQuota); err != nil {
		t.Fatalf("SaveDay missing quota failed: %v", err)
	}

	todaySnapshot := &daySnapshot{
		date:        today,
		startTS:     dayStartTimestamp(today),
		uniqueUsers: map[int64]struct{}{99: {}},
		models: map[string]*dailyPerfStats{
			"gpt-4": {totalRequests: 99, quotaSum: 99},
		},
	}
	if err := hist.SaveDay(todaySnapshot); err != nil {
		t.Fatalf("SaveDay today failed: %v", err)
	}

	_, offset := now.Zone()
	rows, err := hist.QueryDailyAggregatedTrends(7, int64(offset))
	if err != nil {
		t.Fatalf("QueryDailyAggregatedTrends failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("completed channel trend rows=%d, want 1 (today must be excluded): %v", len(rows), rows)
	}
	completedRow := rows[0]
	if toInt64(completedRow["prompt_tokens"]) != 170 || toInt64(completedRow["completion_tokens"]) != 70 ||
		toInt64(completedRow["cache_hit_tokens"]) != 20 || toInt64(completedRow["cache_write_tokens"]) != 10 {
		t.Fatalf("cross-channel Token totals wrong: %v", completedRow)
	}

	if has, err := hist.HasDailyTotals(completedDate); err != nil || !has {
		t.Fatalf("current snapshot must have daily totals: has=%v err=%v", has, err)
	}

	if _, err := hist.db.Exec(`DELETE FROM model_daily_totals WHERE date = ?`, completedDate); err != nil {
		t.Fatalf("delete daily totals failed: %v", err)
	}
	if has, err := hist.HasDate(completedDate); err != nil || !has {
		t.Fatalf("legacy summary must remain visible: has=%v err=%v", has, err)
	}
	if has, err := hist.HasDailyTotals(completedDate); err != nil || has {
		t.Fatalf("legacy summary must be incomplete for catch-up: has=%v err=%v", has, err)
	}
	rows, err = hist.QueryDailyAggregatedTrends(7, int64(offset))
	if err != nil || len(rows) != 1 || toInt64(rows[0]["prompt_tokens"]) != 170 {
		t.Fatalf("channel Token history must not depend on daily totals: rows=%v err=%v", rows, err)
	}

	complete, err := hist.IsFullHistoryBackfillComplete()
	if err != nil || complete {
		t.Fatalf("initial full backfill state=%v err=%v, want incomplete", complete, err)
	}
	dateComplete, err := hist.IsFullHistoryBackfillDateComplete(missingQuotaDate)
	if err != nil || dateComplete {
		t.Fatalf("initial date backfill state=%v err=%v, want incomplete", dateComplete, err)
	}
	if err := hist.MarkFullHistoryBackfillDateComplete(missingQuotaDate); err != nil {
		t.Fatalf("MarkFullHistoryBackfillDateComplete failed: %v", err)
	}
	dateComplete, err = hist.IsFullHistoryBackfillDateComplete(missingQuotaDate)
	if err != nil || !dateComplete {
		t.Fatalf("date backfill state=%v err=%v, want complete", dateComplete, err)
	}
	if err := hist.MarkFullHistoryBackfillComplete(); err != nil {
		t.Fatalf("MarkFullHistoryBackfillComplete failed: %v", err)
	}
	complete, err = hist.IsFullHistoryBackfillComplete()
	if err != nil || !complete {
		t.Fatalf("full backfill state=%v err=%v, want complete", complete, err)
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

func TestValidatePerformanceTimeRange(t *testing.T) {
	tests := []struct {
		name      string
		startTime int64
		endTime   int64
		wantError bool
	}{
		{name: "valid intraday range", startTime: 1_700_000_000, endTime: 1_700_003_600},
		{name: "valid overnight range", startTime: 1_700_000_000, endTime: 1_700_086_400},
		{name: "missing start", startTime: 0, endTime: 1_700_003_600, wantError: true},
		{name: "reversed range", startTime: 1_700_003_600, endTime: 1_700_000_000, wantError: true},
		{name: "more than seven days", startTime: 1_700_000_000, endTime: 1_700_000_000 + 7*24*60*60 + 1, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePerformanceTimeRange(tt.startTime, tt.endTime)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidatePerformanceTimeRange(%d, %d) error=%v, wantError=%v", tt.startTime, tt.endTime, err, tt.wantError)
			}
		})
	}
}
