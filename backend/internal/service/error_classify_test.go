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
		{"429 限速", 429, "", "", "", ErrorBucketRateLimit},
		{"400 格式错误", 400, "", "", "", ErrorBucketUserFormat},
		{"422 格式错误", 422, "", "", "", ErrorBucketUserFormat},
		{"404 格式错误", 404, "", "", "", ErrorBucketUserFormat},
		{"500 模型侧", 500, "", "", "", ErrorBucketModelSide},
		{"503 模型侧", 503, "", "", "", ErrorBucketModelSide},
		{"无状态码-限速关键字", 0, "rate_limit_exceeded", "", "", ErrorBucketRateLimit},
		{"无状态码-overloaded", 0, "", "overloaded_error", "", ErrorBucketRateLimit},
		{"无状态码-格式关键字", 0, "invalid_request_error", "", "", ErrorBucketUserFormat},
		{"无状态码-未知兜底模型侧", 0, "", "", "", ErrorBucketModelSide},
		{"无状态码-未知文本兜底", 0, "weird_thing", "", "", ErrorBucketModelSide},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.classify(tc.statusCode, tc.errorType, tc.upstreamErrType, tc.errCode)
			if got != tc.want {
				t.Errorf("classify(%d,%q,%q,%q) = %q, want %q",
					tc.statusCode, tc.errorType, tc.upstreamErrType, tc.errCode, got, tc.want)
			}
		})
	}
}

func TestClassifyOtherJSONStatusCodeAsString(t *testing.T) {
	rules := DefaultErrorRules()
	// status_code 以字符串存储 (NewAPI 常见)
	if got := rules.classifyOtherJSON(map[string]interface{}{"status_code": "429"}); got != ErrorBucketRateLimit {
		t.Errorf("string 429 => %q, want rate_limit", got)
	}
	// upstream_error_status 兜底
	if got := rules.classifyOtherJSON(map[string]interface{}{"upstream_error_status": float64(500)}); got != ErrorBucketModelSide {
		t.Errorf("upstream 500 => %q, want model_side", got)
	}
}

func TestModelSuccessRate(t *testing.T) {
	// total=100: success=90, model_err=4, format_err=5, rate_limit=1
	// 口径: 分母 = total - format_err = 95 (限速计入), 成功率 = 90/95
	got := modelSuccessRate(90, 100, 5, 1, true)
	if want := 90.0 / 95.0 * 100; got < want-0.001 || got > want+0.001 {
		t.Errorf("modelSuccessRate(限速计入) = %.4f, want %.4f", got, want)
	}

	// 限速不计入模型错误: 分母 = total - format - rate_limit = 94, 成功率 = 90/94
	got2 := modelSuccessRate(90, 100, 5, 1, false)
	if want := 90.0 / 94.0 * 100; got2 < want-0.001 || got2 > want+0.001 {
		t.Errorf("modelSuccessRate(限速不计入) = %.4f, want %.4f", got2, want)
	}

	// 全是用户格式错误: 分母 = 0 → 视为 100% (模型无责任)
	if got3 := modelSuccessRate(0, 10, 10, 0, true); got3 != 100 {
		t.Errorf("全格式错误 = %.4f, want 100", got3)
	}

	// 无请求 → 100%
	if got4 := modelSuccessRate(0, 0, 0, 0, true); got4 != 100 {
		t.Errorf("无请求 = %.4f, want 100", got4)
	}
}
