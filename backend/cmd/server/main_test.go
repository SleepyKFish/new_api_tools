package main

import (
	"reflect"
	"testing"
	"time"
)

func TestCompletedHistoryDatesNewestFirst(t *testing.T) {
	loc := time.Local
	oldest := time.Date(2026, 7, 14, 13, 30, 0, 0, loc)
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, loc)

	got := completedHistoryDatesNewestFirst(oldest, now)
	want := []string{"2026-07-16", "2026-07-15", "2026-07-14"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completed dates=%v, want %v", got, want)
	}
}

func TestCompletedHistoryDatesExcludesToday(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, loc)
	oldest := time.Date(2026, 7, 17, 1, 0, 0, 0, loc)

	if got := completedHistoryDatesNewestFirst(oldest, now); len(got) != 0 {
		t.Fatalf("today must not be included: %v", got)
	}
}
