package service

import (
	"strconv"
	"strings"

	"github.com/new-api-tools/backend/internal/cache"
)

const (
	ErrorBucketModelSide  = "model_side"
	ErrorBucketUserFormat = "user_format"
	ErrorBucketRateLimit  = "rate_limit"
)

const errorRulesCacheKey = "multi_source:error_rules"

type ErrorRuleConfig struct {
	RateLimitStatusCodes       []int    `json:"rate_limit_status_codes"`
	UserFormatStatusCodes      []int    `json:"user_format_status_codes"`
	UserFormatMin              int      `json:"user_format_min"`
	UserFormatMax              int      `json:"user_format_max"`
	RateLimitKeywords          []string `json:"rate_limit_keywords"`
	UserFormatKeywords         []string `json:"user_format_keywords"`
	CountRateLimitAsModelError bool     `json:"count_rate_limit_as_model_error"`
}

func DefaultErrorRules() ErrorRuleConfig {
	return ErrorRuleConfig{
		RateLimitStatusCodes:       []int{429},
		UserFormatStatusCodes:      []int{400, 401, 403, 404, 413, 422},
		UserFormatMin:              400,
		UserFormatMax:              499,
		RateLimitKeywords:          []string{"rate_limit", "rate limit", "too many requests", "overloaded", "quota"},
		UserFormatKeywords:         []string{"invalid_request", "invalid request", "bad request", "validation", "unsupported", "missing"},
		CountRateLimitAsModelError: true,
	}
}

func GetErrorRules() ErrorRuleConfig {
	cm := cache.Get()
	var rules ErrorRuleConfig
	found, _ := cm.GetJSON(errorRulesCacheKey, &rules)
	if !found {
		return DefaultErrorRules()
	}
	return normalizeErrorRules(rules)
}

func SetErrorRules(rules ErrorRuleConfig) {
	cm := cache.Get()
	cm.Set(errorRulesCacheKey, normalizeErrorRules(rules), 0)
}

func normalizeErrorRules(rules ErrorRuleConfig) ErrorRuleConfig {
	def := DefaultErrorRules()
	if len(rules.RateLimitStatusCodes) == 0 {
		rules.RateLimitStatusCodes = def.RateLimitStatusCodes
	}
	if rules.UserFormatMin == 0 && rules.UserFormatMax == 0 && len(rules.UserFormatStatusCodes) == 0 {
		rules.UserFormatStatusCodes = def.UserFormatStatusCodes
		rules.UserFormatMin = def.UserFormatMin
		rules.UserFormatMax = def.UserFormatMax
	}
	return rules
}

func (r ErrorRuleConfig) classify(statusCode int, errorType, upstreamErrType, errCode string) string {
	if statusCode > 0 {
		for _, code := range r.RateLimitStatusCodes {
			if code == statusCode {
				return ErrorBucketRateLimit
			}
		}
		for _, code := range r.UserFormatStatusCodes {
			if code == statusCode {
				return ErrorBucketUserFormat
			}
		}
		if r.UserFormatMin > 0 && r.UserFormatMax >= r.UserFormatMin &&
			statusCode >= r.UserFormatMin && statusCode <= r.UserFormatMax {
			return ErrorBucketUserFormat
		}
		if statusCode >= 500 {
			return ErrorBucketModelSide
		}
	}

	blob := strings.ToLower(strings.Join([]string{errorType, upstreamErrType, errCode}, " "))
	if blob != "" {
		for _, kw := range r.RateLimitKeywords {
			if kw != "" && strings.Contains(blob, strings.ToLower(kw)) {
				return ErrorBucketRateLimit
			}
		}
		for _, kw := range r.UserFormatKeywords {
			if kw != "" && strings.Contains(blob, strings.ToLower(kw)) {
				return ErrorBucketUserFormat
			}
		}
	}

	return ErrorBucketModelSide
}

func (r ErrorRuleConfig) classifyOtherJSON(other map[string]interface{}) string {
	statusCode := extractStatusCode(other)
	errorType := otherText(other, []string{"error_type"})
	upstreamErrType := otherText(other, []string{"upstream_error_type"})
	errCode := otherText(other, []string{"error_code", "upstream_error_code"})
	return r.classify(statusCode, errorType, upstreamErrType, errCode)
}

func extractStatusCode(other map[string]interface{}) int {
	for _, key := range []string{"status_code", "upstream_error_status", "upstream_status_code", "code"} {
		v, ok := other[key]
		if !ok || v == nil {
			continue
		}
		switch val := v.(type) {
		case float64:
			if val > 0 {
				return int(val)
			}
		case int:
			if val > 0 {
				return val
			}
		case int64:
			if val > 0 {
				return int(val)
			}
		case string:
			s := strings.TrimSpace(val)
			if s == "" {
				continue
			}
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func modelSuccessRate(nonEmpty, total, userFormat, rateLimit int64, rules ErrorRuleConfig) float64 {
	denom := modelAvailabilityDenominator(total, userFormat, rateLimit, rules)
	if denom <= 0 {
		return 100
	}
	rate := float64(nonEmpty) / float64(denom) * 100
	if rate < 0 {
		return 0
	}
	if rate > 100 {
		return 100
	}
	return rate
}

func modelAvailabilityDenominator(total, userFormat, rateLimit int64, rules ErrorRuleConfig) int64 {
	denom := total - userFormat
	if !rules.CountRateLimitAsModelError {
		denom -= rateLimit
	}
	if denom < 0 {
		return 0
	}
	return denom
}

func nonFormatFailureCount(failure, formatError int64) int64 {
	if failure <= formatError {
		return 0
	}
	return failure - formatError
}
