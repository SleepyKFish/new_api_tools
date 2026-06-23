package service

import (
	"strconv"
	"strings"

	"github.com/new-api-tools/backend/internal/cache"
)

// 错误分类: 把 NewAPI logs 中 type=5 的失败日志归入三类之一。
//
//	model_side  : 模型/上游侧错误 (5xx、上游 overloaded、网关错误，按口径限速也归此类)
//	user_format : 用户请求格式错误 (4xx 非 429，如 invalid_request_error / 400 / 422)
//	rate_limit  : 限速 (429)
//
// 平台本身不做限速，所以 429 视为模型侧资源不足 —— 成功率分母里 rate_limit 与
// model_side 一起计入，user_format 不计入 (用户自身问题不该拉低模型可用性)。
const (
	ErrorBucketModelSide  = "model_side"
	ErrorBucketUserFormat = "user_format"
	ErrorBucketRateLimit  = "rate_limit"
)

// errorRulesCacheKey 是分类规则在 Redis 中的存储键。规则每请求读取，实现热更新。
const errorRulesCacheKey = "multi_source:error_rules"

// ErrorRuleConfig 是可热更新的错误分类规则。字段全部可在后台编辑。
type ErrorRuleConfig struct {
	// RateLimitStatusCodes: 命中即归为 rate_limit。默认 [429]。
	RateLimitStatusCodes []int `json:"rate_limit_status_codes"`
	// UserFormatStatusCodes: 命中即归为 user_format。默认 [400,401,403,404,413,422]。
	// 注意: 若某状态码同时出现在 RateLimit 与 UserFormat，RateLimit 优先。
	UserFormatStatusCodes []int `json:"user_format_status_codes"`
	// UserFormatMin/Max: 状态码落在 [min,max] 闭区间且未被上面命中，则归为 user_format。
	// 默认 400..499 (429 已被 RateLimit 抢先)。设为 0 表示不启用区间规则。
	UserFormatMin int `json:"user_format_min"`
	UserFormatMax int `json:"user_format_max"`
	// RateLimitKeywords / UserFormatKeywords: 对 error_type / upstream_error_type /
	// error_code 文本做不区分大小写的子串匹配，用于状态码缺失时兜底。
	RateLimitKeywords  []string `json:"rate_limit_keywords"`
	UserFormatKeywords []string `json:"user_format_keywords"`
	// CountRateLimitAsModelError: 限速是否计入成功率分母 (与 model_side 同列)。
	// 默认 true —— 平台不做限速，429 反映上游资源紧张。
	CountRateLimitAsModelError bool `json:"count_rate_limit_as_model_error"`
}

// DefaultErrorRules 返回内置默认规则 (后台未配置时使用)。
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

// GetErrorRules 从 Redis 读取分类规则，未配置则返回默认规则。每请求调用即可热更新。
func GetErrorRules() ErrorRuleConfig {
	cm := cache.Get()
	var rules ErrorRuleConfig
	found, _ := cm.GetJSON(errorRulesCacheKey, &rules)
	if !found {
		return DefaultErrorRules()
	}
	return normalizeErrorRules(rules)
}

// SetErrorRules 持久化分类规则 (TTL=0，永不过期，与现有 model_status 配置一致)。
func SetErrorRules(rules ErrorRuleConfig) {
	cm := cache.Get()
	cm.Set(errorRulesCacheKey, normalizeErrorRules(rules), 0)
}

// normalizeErrorRules 对反序列化后的规则做兜底，避免空规则导致全部错误无法归类。
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

// classifyError 把一条失败日志的错误信号归入三类之一。
//
// 入参从 logs.other 提取:
//
//	statusCode        : other.status_code (优先) / upstream_error_status
//	errorType         : other.error_type
//	upstreamErrType   : other.upstream_error_type
//	errCode           : other.error_code / upstream_error_code
//
// 判定优先级: 状态码精确命中 > 状态码区间 > 关键字 > 兜底 model_side。
func (r ErrorRuleConfig) classify(statusCode int, errorType, upstreamErrType, errCode string) string {
	// 1. 状态码精确命中 (rate_limit 优先于 user_format)
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
		// 2. 状态码区间 (默认 4xx → user_format；5xx 落空 → model_side)
		if r.UserFormatMin > 0 && r.UserFormatMax >= r.UserFormatMin &&
			statusCode >= r.UserFormatMin && statusCode <= r.UserFormatMax {
			return ErrorBucketUserFormat
		}
		if statusCode >= 500 {
			return ErrorBucketModelSide
		}
	}

	// 3. 关键字兜底 (状态码缺失或未命中时)
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

	// 4. 兜底: 无法识别的失败一律算模型侧 (保守，不让未知错误抬高成功率)
	return ErrorBucketModelSide
}

// classifyOtherJSON 从已解析的 other map 中提取错误信号并分类。
func (r ErrorRuleConfig) classifyOtherJSON(other map[string]interface{}) string {
	statusCode := extractStatusCode(other)
	errorType := otherText(other, []string{"error_type"})
	upstreamErrType := otherText(other, []string{"upstream_error_type"})
	errCode := otherText(other, []string{"error_code", "upstream_error_code"})
	return r.classify(statusCode, errorType, upstreamErrType, errCode)
}

// extractStatusCode 从 other 中读取 HTTP 状态码，兼容 string / number 两种存储。
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
