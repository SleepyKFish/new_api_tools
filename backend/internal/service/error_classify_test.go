package service

import "testing"

func TestClassifyError(t *testing.T) {
	rules := DefaultErrorRules()

	cases := []struct {
		name            string
		statusCode      int
		errorType       string
		upstreamErrType string
		errCode         string
		want            string
	}{
		{"429 rate limit", 429, "", "", "", ErrorBucketRateLimit},
		{"400 user format", 400, "", "", "", ErrorBucketUserFormat},
		{"422 user format", 422, "", "", "", ErrorBucketUserFormat},
		{"500 model side", 500, "", "", "", ErrorBucketModelSide},
		{"rate keyword", 0, "rate_limit_exceeded", "", "", ErrorBucketRateLimit},
		{"format keyword", 0, "invalid_request_error", "", "", ErrorBucketUserFormat},
		{"unknown", 0, "upstream_closed", "", "", ErrorBucketModelSide},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.classify(tc.statusCode, tc.errorType, tc.upstreamErrType, tc.errCode)
			if got != tc.want {
				t.Fatalf("classify()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyOtherJSONStatusCodeAsString(t *testing.T) {
	rules := DefaultErrorRules()

	if got := rules.classifyOtherJSON(map[string]interface{}{"status_code": "429"}); got != ErrorBucketRateLimit {
		t.Fatalf("string status 429=%q want %q", got, ErrorBucketRateLimit)
	}
	if got := rules.classifyOtherJSON(map[string]interface{}{"upstream_error_status": float64(500)}); got != ErrorBucketModelSide {
		t.Fatalf("upstream 500=%q want %q", got, ErrorBucketModelSide)
	}
}

func TestModelSuccessRate(t *testing.T) {
	rules := DefaultErrorRules()
	got := modelSuccessRate(90, 100, 5, 1, rules)
	want := 90.0 / 95.0 * 100
	if got < want-0.001 || got > want+0.001 {
		t.Fatalf("modelSuccessRate=%.4f want %.4f", got, want)
	}

	rules.CountRateLimitAsModelError = false
	got = modelSuccessRate(90, 100, 5, 1, rules)
	want = 90.0 / 94.0 * 100
	if got < want-0.001 || got > want+0.001 {
		t.Fatalf("modelSuccessRate without rate limit=%.4f want %.4f", got, want)
	}

	if got := modelSuccessRate(0, 10, 10, 0, DefaultErrorRules()); got != 100 {
		t.Fatalf("all user format errors=%.4f want 100", got)
	}
}
