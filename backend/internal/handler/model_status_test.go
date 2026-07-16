package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParsePerformanceTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name      string
		query     string
		wantStart int64
		wantEnd   int64
		wantRange bool
		wantError bool
	}{
		{name: "window query without range"},
		{name: "valid range", query: "start_time=1700000000&end_time=1700003600", wantStart: 1_700_000_000, wantEnd: 1_700_003_600, wantRange: true},
		{name: "missing end", query: "start_time=1700000000", wantError: true},
		{name: "invalid start", query: "start_time=nope&end_time=1700003600", wantError: true},
		{name: "reversed range", query: "start_time=1700003600&end_time=1700000000", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil)

			startTime, endTime, hasRange, err := parsePerformanceTimeRange(ctx)
			if (err != nil) != tt.wantError {
				t.Fatalf("parsePerformanceTimeRange() error=%v, wantError=%v", err, tt.wantError)
			}
			if startTime != tt.wantStart || endTime != tt.wantEnd || hasRange != tt.wantRange {
				t.Fatalf(
					"parsePerformanceTimeRange()=(%d, %d, %v), want (%d, %d, %v)",
					startTime, endTime, hasRange, tt.wantStart, tt.wantEnd, tt.wantRange,
				)
			}
		})
	}
}
