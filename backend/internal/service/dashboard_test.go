package service

import (
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
