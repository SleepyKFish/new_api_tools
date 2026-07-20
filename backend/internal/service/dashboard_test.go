package service

import (
	"errors"
	"testing"
	"time"
)

func TestPreviousCompleteHourRange(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.July, 17, 10, 37, 42, 0, loc)

	start, end := previousCompleteHourRange(now)

	if want := time.Date(2026, time.July, 17, 9, 0, 0, 0, loc); !start.Equal(want) {
		t.Fatalf("start = %v, want %v", start, want)
	}
	if want := time.Date(2026, time.July, 17, 10, 0, 0, 0, loc); !end.Equal(want) {
		t.Fatalf("end = %v, want %v", end, want)
	}
}

func TestCurrentHourRange(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.July, 17, 10, 37, 42, 0, loc)

	start, end := currentHourRange(now)

	if want := time.Date(2026, time.July, 17, 10, 0, 0, 0, loc); !start.Equal(want) {
		t.Fatalf("start = %v, want %v", start, want)
	}
	if !end.Equal(now) {
		t.Fatalf("end = %v, want %v", end, now)
	}
}

func TestCompletedTodayRange(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.July, 17, 10, 37, 42, 0, loc)

	start, end, hours := completedTodayRange(now)
	if want := time.Date(2026, time.July, 17, 0, 0, 0, 0, loc); !start.Equal(want) {
		t.Fatalf("start = %v, want %v", start, want)
	}
	if want := time.Date(2026, time.July, 17, 10, 0, 0, 0, loc); !end.Equal(want) {
		t.Fatalf("end = %v, want %v", end, want)
	}
	if hours != 10 {
		t.Fatalf("hours = %d, want 10", hours)
	}

	_, midnightEnd, midnightHours := completedTodayRange(start)
	if !midnightEnd.Equal(start) || midnightHours != 0 {
		t.Fatalf("midnight range end=%v hours=%d, want end=%v hours=0", midnightEnd, midnightHours, start)
	}
}

func TestCompletedHourRange(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.July, 17, 10, 37, 42, 0, loc)
	validStart := time.Date(2026, time.July, 17, 9, 0, 0, 0, loc)

	start, end, err := completedHourRange(validStart.Unix(), now)
	if err != nil {
		t.Fatalf("completedHourRange() error = %v", err)
	}
	if !start.Equal(validStart) || !end.Equal(validStart.Add(time.Hour)) {
		t.Fatalf("range = [%v, %v), want [%v, %v)", start, end, validStart, validStart.Add(time.Hour))
	}

	invalidStarts := []time.Time{
		time.Date(2026, time.July, 17, 9, 30, 0, 0, loc),
		time.Date(2026, time.July, 17, 10, 0, 0, 0, loc),
		time.Date(2026, time.July, 16, 23, 0, 0, 0, loc),
	}
	for _, invalidStart := range invalidStarts {
		if _, _, err := completedHourRange(invalidStart.Unix(), now); !errors.Is(err, ErrInvalidCompletedHour) {
			t.Errorf("completedHourRange(%v) error = %v, want ErrInvalidCompletedHour", invalidStart, err)
		}
	}
}

func TestFillHourlyGapsAtUsesRequestedHour(t *testing.T) {
	const tzOffset = 8 * 60 * 60
	loc := time.FixedZone("UTC+8", tzOffset)
	anchor := time.Date(2026, time.July, 17, 9, 0, 0, 0, loc)
	hourGroup := (anchor.Unix() + int64(tzOffset)) / 3600
	rows := []map[string]interface{}{{
		"hour_group":    hourGroup,
		"request_count": int64(12),
		"quota_used":    int64(345),
	}}

	result := fillHourlyGapsAt(rows, 1, tzOffset, anchor)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if got, want := result[0]["hour"], "2026-07-17 09:00"; got != want {
		t.Fatalf("hour = %v, want %v", got, want)
	}
	if got := toInt64(result[0]["request_count"]); got != 12 {
		t.Fatalf("request_count = %d, want 12", got)
	}
}

func TestMergeDailyTokenMetricsPreservesQuotaDataMetrics(t *testing.T) {
	base := []map[string]interface{}{
		{
			"day_group":         int64(100),
			"request_count":     int64(7),
			"quota_used":        int64(500),
			"unique_users":      int64(3),
			"prompt_tokens":     int64(0),
			"completion_tokens": int64(0),
		},
		{
			"day_group":         int64(101),
			"request_count":     int64(4),
			"quota_used":        int64(250),
			"unique_users":      int64(2),
			"prompt_tokens":     int64(0),
			"completion_tokens": int64(0),
		},
	}
	tokens := []map[string]interface{}{
		{
			"day_group":          int64(100),
			"quota_used":         int64(999),
			"prompt_tokens":      int64(120),
			"completion_tokens":  int64(40),
			"cache_hit_tokens":   int64(20),
			"cache_write_tokens": int64(10),
		},
		{
			"day_group":         int64(99),
			"prompt_tokens":     int64(50),
			"completion_tokens": int64(15),
		},
	}

	got := mergeDailyTokenMetrics(base, tokens)
	if len(got) != 3 || toInt64(got[0]["day_group"]) != 99 ||
		toInt64(got[1]["day_group"]) != 100 || toInt64(got[2]["day_group"]) != 101 {
		t.Fatalf("merged rows not sorted or complete: %v", got)
	}
	merged := got[1]
	if toInt64(merged["quota_used"]) != 500 || toInt64(merged["request_count"]) != 7 || toInt64(merged["unique_users"]) != 3 {
		t.Fatalf("quota_data metrics were overwritten: %v", merged)
	}
	if toInt64(merged["prompt_tokens"]) != 120 || toInt64(merged["completion_tokens"]) != 40 ||
		toInt64(merged["cache_hit_tokens"]) != 20 || toInt64(merged["cache_write_tokens"]) != 10 {
		t.Fatalf("token metrics were not overlaid: %v", merged)
	}
	if toInt64(got[0]["quota_used"]) != 0 || toInt64(got[0]["prompt_tokens"]) != 50 {
		t.Fatalf("history-only day should retain zero cost and token metrics: %v", got[0])
	}
	if toInt64(got[2]["quota_used"]) != 250 || toInt64(got[2]["prompt_tokens"]) != 0 {
		t.Fatalf("quota-only day should retain cost and unavailable token metrics: %v", got[2])
	}
}
