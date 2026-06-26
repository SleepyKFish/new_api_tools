package service

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

// Constants for model status
var (
	AvailableTimeWindows = []string{"15m", "30m", "1h", "6h", "12h", "24h"}
	DefaultTimeWindow    = "24h"
	AvailableThemes      = []string{
		"daylight", "obsidian", "minimal", "neon", "forest", "ocean", "terminal",
		"cupertino", "material", "openai", "anthropic", "vercel", "linear",
		"stripe", "github", "discord", "tesla",
	}
	DefaultTheme = "daylight"
	// LegacyThemeMap maps old theme names to valid ones
	LegacyThemeMap = map[string]string{
		"light":  "daylight",
		"dark":   "obsidian",
		"system": "daylight",
	}
	AvailableRefreshIntervals = []int{0, 30, 60, 120, 300}
	AvailableSortModes        = []string{"default", "availability", "custom"}
	performanceLogBatchSize   = 5000
	modelStatusRealtimeCacheTTL = 30 * time.Second
	modelHistoryBatchInterval = 2 * time.Minute
)

// Time window slot configurations: {totalSeconds, numSlots, slotSeconds}
type timeWindowConfig struct {
	totalSeconds int64
	numSlots     int
	slotSeconds  int64
}

var timeWindowConfigs = map[string]timeWindowConfig{
	"15m": {900, 15, 60},     // 15 minutes, 15 slots, 1 minute each
	"30m": {1800, 30, 60},    // 30 minutes, 30 slots, 1 minute each
	"1h":  {3600, 60, 60},    // 1 hour, 60 slots, 1 minute each
	"6h":  {21600, 24, 900},  // 6 hours, 24 slots, 15 minutes each
	"12h": {43200, 24, 1800}, // 12 hours, 24 slots, 30 minutes each
	"24h": {86400, 24, 3600}, // 24 hours, 24 slots, 1 hour each
}

var customTimeWindowPattern = regexp.MustCompile(`^([1-9]\d*)(min|m|h)$`)

func ParseTimeWindow(window string) (timeWindowConfig, bool) {
	if cfg, ok := timeWindowConfigs[window]; ok {
		return cfg, true
	}

	matches := customTimeWindowPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(window)))
	if matches == nil {
		return timeWindowConfig{}, false
	}
	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return timeWindowConfig{}, false
	}

	var totalSeconds int64
	switch matches[2] {
	case "min", "m":
		totalSeconds = int64(value) * 60
	case "h":
		totalSeconds = int64(value) * 3600
	default:
		return timeWindowConfig{}, false
	}
	if totalSeconds < 60 || totalSeconds > 7*24*3600 {
		return timeWindowConfig{}, false
	}

	return timeWindowConfig{
		totalSeconds: totalSeconds,
		numSlots:     chooseTimeWindowSlotCount(totalSeconds),
		slotSeconds:  chooseTimeWindowSlotSeconds(totalSeconds),
	}, true
}

func IsValidTimeWindow(window string) bool {
	_, ok := ParseTimeWindow(window)
	return ok
}

func NormalizeTimeWindow(window string) string {
	window = strings.ToLower(strings.TrimSpace(window))
	if _, ok := timeWindowConfigs[window]; ok {
		return window
	}
	if strings.HasSuffix(window, "m") && !strings.HasSuffix(window, "min") {
		return strings.TrimSuffix(window, "m") + "min"
	}
	return window
}

func chooseTimeWindowSlotCount(totalSeconds int64) int {
	switch {
	case totalSeconds <= 3600:
		return int(totalSeconds / 60)
	default:
		return 24
	}
}

func chooseTimeWindowSlotSeconds(totalSeconds int64) int64 {
	slots := chooseTimeWindowSlotCount(totalSeconds)
	if slots <= 0 {
		return 60
	}
	slotSeconds := totalSeconds / int64(slots)
	if slotSeconds < 60 {
		return 60
	}
	return slotSeconds
}

// getStatusColor determines status color based on success rate (matches Python backend)
func getStatusColor(successRate float64, totalRequests int64) string {
	if totalRequests == 0 {
		return "green" // No requests = no issues
	}
	if successRate >= 95 {
		return "green"
	} else if successRate >= 80 {
		return "yellow"
	}
	return "red"
}

// roundRate rounds a float to 2 decimal places
func roundRate(rate float64) float64 {
	return math.Round(rate*100) / 100
}

func roundTokenCount(tokens float64) int64 {
	if tokens <= 0 {
		return 0
	}
	return int64(math.Round(tokens))
}

func buildPerformanceSummary(totalRequests, timedRequests, outputRequests, within5s, within10s, durationTimedRequests, durationWithin10s, durationWithin20s, claudeRequests int64, cacheDenominatorSum, cacheTokensSum, cacheWriteSum, cacheWriteTokensSum, inputTokensSum, outputTokensSum, completionTokensSum, useTimeSum float64) map[string]interface{} {
	var within5sRate interface{}
	var within10sRate interface{}
	var durationWithin10sRate interface{}
	var durationWithin20sRate interface{}
	var cacheHitRate interface{}
	var cacheWriteRate interface{}
	var completionTPS interface{}

	if timedRequests > 0 {
		within5sRate = roundRate(float64(within5s) / float64(timedRequests) * 100)
		within10sRate = roundRate(float64(within10s) / float64(timedRequests) * 100)
	}
	if durationTimedRequests > 0 {
		durationWithin10sRate = roundRate(float64(durationWithin10s) / float64(durationTimedRequests) * 100)
		durationWithin20sRate = roundRate(float64(durationWithin20s) / float64(durationTimedRequests) * 100)
	}
	if cacheDenominatorSum > 0 {
		cacheHitTokens := cacheTokensSum
		if cacheHitTokens > cacheDenominatorSum {
			cacheHitTokens = cacheDenominatorSum
		}
		cacheHitRate = roundRate(cacheHitTokens / cacheDenominatorSum * 100)
	}
	// cache_write_rate 仅在存在 Claude Messages 请求时有意义,
	// 否则保留 nil 让前端展示 N/A。
	if claudeRequests > 0 && cacheDenominatorSum > 0 {
		cacheWriteTokens := cacheWriteSum
		if cacheWriteTokens > cacheDenominatorSum {
			cacheWriteTokens = cacheDenominatorSum
		}
		cacheWriteRate = roundRate(cacheWriteTokens / cacheDenominatorSum * 100)
	}
	if useTimeSum > 0 {
		completionTPS = roundRate(completionTokensSum / useTimeSum)
	}

	return map[string]interface{}{
		"total_requests":           totalRequests,
		"within_5s_rate":           within5sRate,
		"within_10s_rate":          within10sRate,
		"duration_within_10s_rate": durationWithin10sRate,
		"duration_within_20s_rate": durationWithin20sRate,
		"cache_hit_rate":           cacheHitRate,
		"cache_write_rate":         cacheWriteRate,
		"cache_hit_tokens":         roundTokenCount(cacheTokensSum),
		"cache_write_tokens":       roundTokenCount(cacheWriteTokensSum),
		"total_input_tokens":       roundTokenCount(inputTokensSum),
		"total_output_tokens":      roundTokenCount(outputTokensSum),
		"completion_tps":           completionTPS,
		"timed_requests":           timedRequests,
		"duration_timed_requests":  durationTimedRequests,
		"output_requests":          outputRequests,
	}
}

func (s *ModelStatusService) firstTokenTimeExpr(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	useTime := prefix + "use_time"
	if !s.db.ColumnExists("logs", "is_stream") || !s.db.ColumnExists("logs", "other") {
		return useTime
	}

	other := prefix + "other"
	isStream := prefix + "is_stream"
	if s.db.IsPG {
		frtMS := fmt.Sprintf("CAST(CAST(NULLIF(NULLIF(%s, ''), 'null') AS jsonb)->>'frt' AS DOUBLE PRECISION)", other)
		return fmt.Sprintf(`CASE
			WHEN COALESCE(%s, false) = true
				AND %s IS NOT NULL
				AND %s <> ''
				AND %s <> 'null'
				AND COALESCE(%s, 0) > 0
			THEN %s / 1000.0
			ELSE %s
		END`, isStream, other, other, other, frtMS, frtMS, useTime)
	}

	frtMS := fmt.Sprintf("CAST(JSON_UNQUOTE(JSON_EXTRACT(%s, '$.frt')) AS DECIMAL(20,6))", other)
	return fmt.Sprintf(`CASE
		WHEN COALESCE(%s, 0) = 1
			AND %s IS NOT NULL
			AND %s <> ''
			AND JSON_VALID(%s)
			AND COALESCE(%s, 0) > 0
		THEN %s / 1000.0
		ELSE %s
	END`, isStream, other, other, other, frtMS, frtMS, useTime)
}

func (s *ModelStatusService) jsonNumberExpr(jsonExpr, key string) string {
	if s.db.IsPG {
		return fmt.Sprintf(`CASE
			WHEN %s IS NOT NULL AND %s <> '' AND %s <> 'null'
			THEN CAST(CAST(%s AS jsonb)->>'%s' AS DOUBLE PRECISION)
			ELSE 0
		END`, jsonExpr, jsonExpr, jsonExpr, jsonExpr, key)
	}
	return fmt.Sprintf(`CASE
		WHEN %s IS NOT NULL AND %s <> '' AND JSON_VALID(%s)
		THEN CAST(JSON_UNQUOTE(JSON_EXTRACT(%s, '$.%s')) AS DECIMAL(20,6))
		ELSE 0
	END`, jsonExpr, jsonExpr, jsonExpr, jsonExpr, key)
}

func (s *ModelStatusService) jsonTextExpr(jsonExpr, key string) string {
	if s.db.IsPG {
		return fmt.Sprintf(`CASE
			WHEN %s IS NOT NULL AND %s <> '' AND %s <> 'null'
			THEN CAST(%s AS jsonb)->>'%s'
			ELSE NULL
		END`, jsonExpr, jsonExpr, jsonExpr, jsonExpr, key)
	}
	return fmt.Sprintf(`CASE
		WHEN %s IS NOT NULL AND %s <> '' AND JSON_VALID(%s)
		THEN JSON_UNQUOTE(JSON_EXTRACT(%s, '$.%s'))
		ELSE NULL
	END`, jsonExpr, jsonExpr, jsonExpr, jsonExpr, key)
}

func (s *ModelStatusService) errorStatusCodeExpr(otherCol string) string {
	if !s.db.ColumnExists("logs", "other") {
		return "0"
	}

	keys := []string{"status_code", "upstream_error_status", "upstream_status_code", "code"}
	if s.db.IsPG {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			textExpr := s.jsonTextExpr(otherCol, key)
			parts = append(parts, fmt.Sprintf(`CASE
				WHEN (%s) ~ '^[0-9]+$' THEN CAST((%s) AS INTEGER)
				ELSE 0
			END`, textExpr, textExpr))
		}
		return fmt.Sprintf("GREATEST(%s)", strings.Join(parts, ", "))
	}

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("COALESCE((%s), 0)", s.jsonNumberExpr(otherCol, key)))
	}
	return fmt.Sprintf("GREATEST(%s)", strings.Join(parts, ", "))
}

func intListSQL(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

func (s *ModelStatusService) errorKeywordCondition(otherCol string, keywords []string) string {
	if !s.db.ColumnExists("logs", "other") || len(keywords) == 0 {
		return "FALSE"
	}

	blob := fmt.Sprintf("LOWER(CONCAT_WS(' ', COALESCE(%s, ''), COALESCE(%s, ''), COALESCE(%s, ''), COALESCE(%s, '')))",
		s.jsonTextExpr(otherCol, "error_type"),
		s.jsonTextExpr(otherCol, "upstream_error_type"),
		s.jsonTextExpr(otherCol, "error_code"),
		s.jsonTextExpr(otherCol, "upstream_error_code"))
	if s.db.IsPG {
		blob = fmt.Sprintf("LOWER(CONCAT_WS(' ', COALESCE(%s, ''), COALESCE(%s, ''), COALESCE(%s, ''), COALESCE(%s, '')))",
			s.jsonTextExpr(otherCol, "error_type"),
			s.jsonTextExpr(otherCol, "upstream_error_type"),
			s.jsonTextExpr(otherCol, "error_code"),
			s.jsonTextExpr(otherCol, "upstream_error_code"))
	}

	conditions := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		kw = strings.TrimSpace(strings.ToLower(kw))
		if kw == "" {
			continue
		}
		conditions = append(conditions, fmt.Sprintf("%s LIKE '%%%s%%'", blob, strings.ReplaceAll(kw, "'", "''")))
	}
	if len(conditions) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(conditions, " OR ") + ")"
}

func (s *ModelStatusService) rateLimitErrorSQLCondition(otherCol string, rules ErrorRuleConfig) string {
	if !s.db.ColumnExists("logs", "other") {
		return "FALSE"
	}
	statusCode := s.errorStatusCodeExpr(otherCol)
	conditions := []string{}
	if codes := intListSQL(rules.RateLimitStatusCodes); codes != "" {
		conditions = append(conditions, fmt.Sprintf("(%s) IN (%s)", statusCode, codes))
	}
	conditions = append(conditions, s.errorKeywordCondition(otherCol, rules.RateLimitKeywords))
	return "(" + strings.Join(conditions, " OR ") + ")"
}

func (s *ModelStatusService) userFormatErrorSQLCondition(otherCol string, rules ErrorRuleConfig) string {
	if !s.db.ColumnExists("logs", "other") {
		return "FALSE"
	}
	statusCode := s.errorStatusCodeExpr(otherCol)
	conditions := []string{}
	if codes := intListSQL(rules.UserFormatStatusCodes); codes != "" {
		conditions = append(conditions, fmt.Sprintf("(%s) IN (%s)", statusCode, codes))
	}
	if rules.UserFormatMin > 0 && rules.UserFormatMax >= rules.UserFormatMin {
		conditions = append(conditions, fmt.Sprintf("((%s) >= %d AND (%s) <= %d)", statusCode, rules.UserFormatMin, statusCode, rules.UserFormatMax))
	}
	conditions = append(conditions, s.errorKeywordCondition(otherCol, rules.UserFormatKeywords))
	return fmt.Sprintf("((%s) AND NOT %s)", strings.Join(conditions, " OR "), s.rateLimitErrorSQLCondition(otherCol, rules))
}

func (s *ModelStatusService) requestPathExpr(otherCol string) string {
	keys := []string{
		"请求路径",
		"request_path",
		"path",
		"endpoint",
		"url",
		"request_url",
	}
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		parts = append(parts, s.jsonTextExpr(otherCol, key))
	}
	parts = append(parts, "''")
	return fmt.Sprintf("LOWER(COALESCE(%s))", strings.Join(parts, ", "))
}

func (s *ModelStatusService) requestConversionExpr(otherCol string) string {
	keys := []string{
		"request_conversion",
		"conversion",
		"request_format",
		"request_relay_format",
		"final_request_relay_format",
		"relay_format",
	}
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		parts = append(parts, s.jsonTextExpr(otherCol, key))
	}
	parts = append(parts, "''")
	return fmt.Sprintf("LOWER(COALESCE(%s))", strings.Join(parts, ", "))
}

func (s *ModelStatusService) jsonBoolExpr(jsonExpr, key string) string {
	return fmt.Sprintf("LOWER(COALESCE(%s, '')) IN ('true', '1')", s.jsonTextExpr(jsonExpr, key))
}

// cacheWriteTokensExpr returns a SQL expression for "缓存写 tokens" with the
// same three-level fallback the frontend uses in getUsageLogCacheSummary:
//
//  1. other.cache_write_tokens (后端归一化后的总量) — 优先使用
//  2. cache_creation_tokens_5m + cache_creation_tokens_1h — 当存在拆分时取
//     max(拆分之和, cache_creation_tokens) 以兼容旧/新两种写法
//  3. other.cache_creation_tokens — 兜底
func (s *ModelStatusService) cacheWriteTokensExpr(otherCol string) string {
	write := s.jsonNumberExpr(otherCol, "cache_write_tokens")
	creation := s.jsonNumberExpr(otherCol, "cache_creation_tokens")
	creation5m := s.jsonNumberExpr(otherCol, "cache_creation_tokens_5m")
	creation1h := s.jsonNumberExpr(otherCol, "cache_creation_tokens_1h")
	return fmt.Sprintf(`CASE
		WHEN (%s) > 0 THEN (%s)
		WHEN ((%s) + (%s)) > 0 THEN GREATEST((%s) + (%s), (%s))
		ELSE (%s)
	END`, write, write, creation5m, creation1h, creation5m, creation1h, creation, creation)
}

func (s *ModelStatusService) cacheTokensSumSelect(alias string) string {
	if !s.db.ColumnExists("logs", "other") {
		return "0 as cache_tokens_sum"
	}

	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	typeCol := prefix + "type"
	otherCol := prefix + "other"
	cacheTokensExpr := s.jsonNumberExpr(otherCol, "cache_tokens")
	return fmt.Sprintf(`COALESCE(SUM(CASE
		WHEN %s = 2
			AND %s IS NOT NULL
			AND %s <> ''
			AND (%s) > 0
		THEN (%s)
		ELSE 0
	END), 0) as cache_tokens_sum`, typeCol, otherCol, otherCol, cacheTokensExpr, cacheTokensExpr)
}

func (s *ModelStatusService) cacheDenominatorSumSelect(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	typeCol := prefix + "type"
	promptCol := prefix + "prompt_tokens"
	if !s.db.ColumnExists("logs", "other") {
		return fmt.Sprintf("COALESCE(SUM(CASE WHEN %s = 2 AND %s > 0 THEN %s ELSE 0 END), 0) as cache_denominator_sum", typeCol, promptCol, promptCol)
	}

	otherCol := prefix + "other"
	cacheTokensExpr := s.jsonNumberExpr(otherCol, "cache_tokens")
	cacheWriteExpr := s.cacheWriteTokensExpr(otherCol)
	requestPathExpr := s.requestPathExpr(otherCol)
	requestConversionExpr := s.requestConversionExpr(otherCol)
	isClaudeExpr := s.jsonBoolExpr(otherCol, "claude")
	// Claude/Anthropic 语义: prompt_tokens 既不含缓存读也不含缓存写,所以分母 = 非缓存输入 + 缓存读 + 缓存写
	// OpenAI-like: prompt_tokens 通常已含缓存读,缓存写罕见,沿用 MAX(prompt, cache_tokens) 近似
	return fmt.Sprintf(`COALESCE(SUM(CASE
		WHEN %s = 2
		THEN CASE
			WHEN %s
				OR %s LIKE '%%-> claude messages%%'
				OR %s LIKE '%%->claude messages%%'
				OR %s = 'claude messages'
				OR (%s = '' AND %s LIKE '%%/v1/messages%%')
			THEN COALESCE(%s, 0) + COALESCE((%s), 0) + COALESCE((%s), 0)
			ELSE GREATEST(COALESCE(%s, 0), COALESCE((%s), 0))
		END
		ELSE 0
	END), 0) as cache_denominator_sum`, typeCol, isClaudeExpr, requestConversionExpr, requestConversionExpr, requestConversionExpr, requestConversionExpr, requestPathExpr, promptCol, cacheTokensExpr, cacheWriteExpr, promptCol, cacheTokensExpr)
}

// ModelStatusService handles model availability monitoring
type ModelStatusService struct {
	db *database.Manager
}

type performanceStats struct {
	successRequests       int64
	timedRequests         int64
	within5s              int64
	within10s             int64
	durationTimedRequests int64
	durationWithin10s     int64
	durationWithin20s     int64
	outputRequests        int64
	cacheDenominatorSum   float64
	cacheTokensSum        float64
	cacheWriteSum         float64
	cacheWriteTokensSum   float64
	inputTokensSum        float64
	outputTokensSum       float64
	claudeRequests        int64
	completionTokensSum   float64
	useTimeSum            float64
}

// NewModelStatusService creates a new ModelStatusService
func NewModelStatusService() *ModelStatusService {
	return &ModelStatusService{db: database.Get()}
}

func performanceSummaryFromStats(stats *performanceStats) map[string]interface{} {
	if stats == nil {
		stats = &performanceStats{}
	}
	return buildPerformanceSummary(
		stats.successRequests,
		stats.timedRequests,
		stats.outputRequests,
		stats.within5s,
		stats.within10s,
		stats.durationTimedRequests,
		stats.durationWithin10s,
		stats.durationWithin20s,
		stats.claudeRequests,
		stats.cacheDenominatorSum,
		stats.cacheTokensSum,
		stats.cacheWriteSum,
		stats.cacheWriteTokensSum,
		stats.inputTokensSum,
		stats.outputTokensSum,
		stats.completionTokensSum,
		stats.useTimeSum,
	)
}

func toBool(v interface{}) bool {
	switch value := v.(type) {
	case bool:
		return value
	case int, int64, int32, uint64, float64, float32:
		return toFloat64(value) != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "y", "on":
			return true
		}
	}
	return false
}

func parseOtherJSON(v interface{}) map[string]interface{} {
	if other, ok := v.(map[string]interface{}); ok {
		return other
	}
	raw := strings.TrimSpace(toString(v))
	if raw == "" || strings.EqualFold(raw, "null") {
		return map[string]interface{}{}
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]interface{}{}
	}
	if parsed == nil {
		return map[string]interface{}{}
	}
	return parsed
}

func otherText(other map[string]interface{}, keys []string) string {
	for _, key := range keys {
		value, ok := other[key]
		if !ok || value == nil {
			continue
		}
		if items, ok := value.([]interface{}); ok {
			parts := make([]string, 0, len(items))
			for _, item := range items {
				if item != nil {
					parts = append(parts, fmt.Sprint(item))
				}
			}
			return strings.Join(parts, " -> ")
		}
		return fmt.Sprint(value)
	}
	return ""
}

func isClaudeMessagesRequest(other map[string]interface{}) bool {
	if toBool(other["claude"]) {
		return true
	}

	var conversionValue interface{}
	for _, key := range []string{
		"request_conversion",
		"conversion",
		"request_format",
		"request_relay_format",
		"final_request_relay_format",
		"relay_format",
	} {
		if value, ok := other[key]; ok && value != nil {
			conversionValue = value
			break
		}
	}

	if items, ok := conversionValue.([]interface{}); ok {
		for i := len(items) - 1; i >= 0; i-- {
			if items[i] == nil {
				continue
			}
			return strings.EqualFold(strings.TrimSpace(fmt.Sprint(items[i])), "claude messages")
		}
	} else if conversionValue != nil {
		conversionText := strings.ToLower(strings.TrimSpace(fmt.Sprint(conversionValue)))
		if conversionText == "claude messages" ||
			strings.Contains(conversionText, "-> claude messages") ||
			strings.Contains(conversionText, "->claude messages") {
			return true
		}
	}

	requestPath := strings.ToLower(otherText(other, []string{
		"请求路径",
		"request_path",
		"path",
		"endpoint",
		"url",
		"request_url",
	}))
	return strings.Contains(requestPath, "/v1/messages")
}

func inputTokensFromLog(promptTokens, cacheTokens, cacheWriteTokens float64, isClaude bool, other map[string]interface{}) float64 {
	if explicit := toFloat64(other["input_tokens_total"]); explicit > 0 {
		return explicit
	}
	if isClaude {
		return promptTokens + cacheTokens + cacheWriteTokens
	}
	return math.Max(promptTokens, cacheTokens)
}

// rowMetrics 是一条日志解析一次后的全部派生结果,供多个维度桶(模型/渠道/渠道模型
// 的可用性与性能)共享累加,避免单次扫描内对同一行重复解析 other JSON、重复计算派生量。
// 可用性字段语义对齐 accumulateMetrics,性能字段对齐 accumulatePerformanceMetrics。
type rowMetrics struct {
	valid     bool  // type ∈ {2,5}
	createdAt int64 // 用于定位 slot

	// 可用性维度
	isSuccess   bool   // type==2 && completion>0
	isFailure   bool   // type==5
	isEmpty     bool   // type==2 && completion==0
	errorBucket string // 仅 type==5: ErrorBucketUserFormat / ErrorBucketRateLimit / ""

	// 性能维度(仅 type==2 有意义;isPerf 对应旧 successRequests 的自增条件)
	isPerf                bool
	timedRequests         int64
	within5s              int64
	within10s             int64
	durationTimedRequests int64
	durationWithin10s     int64
	durationWithin20s     int64
	outputRequests        int64
	claudeRequests        int64
	cacheDenominatorSum   float64
	cacheTokensSum        float64
	cacheWriteSum         float64
	cacheWriteTokensSum   float64
	inputTokensSum        float64
	outputTokensSum       float64
	completionTokensSum   float64
	useTimeSum            float64
}

// computeRowMetrics 解析一条 row(含 other JSON)一次,产出所有派生指标。
// 这是各扫描路径(实时模型/渠道/渠道模型、历史每日聚合)共享的唯一解析入口。
func computeRowMetrics(row map[string]interface{}, rules ErrorRuleConfig) rowMetrics {
	logType := toInt64(row["type"])
	if logType != 2 && logType != 5 {
		return rowMetrics{}
	}

	m := rowMetrics{
		valid:     true,
		createdAt: toInt64(row["created_at"]),
	}

	completion := toFloat64(row["completion_tokens"])
	switch {
	case logType == 2 && completion > 0:
		m.isSuccess = true
	case logType == 5:
		m.isFailure = true
		m.errorBucket = rules.classifyOtherJSON(parseOtherJSON(row["other"]))
	case logType == 2 && completion == 0:
		m.isEmpty = true
	}

	if logType != 2 {
		return m
	}

	// --- 性能维度(仅 type==2)---
	m.isPerf = true
	useTime := toFloat64(row["use_time"])
	promptTokens := toFloat64(row["prompt_tokens"])
	completionTokens := completion
	isStream := toBool(row["is_stream"])
	other := parseOtherJSON(row["other"])
	frtSeconds := toFloat64(other["frt"]) / 1000.0

	firstTokenTime := useTime
	if isStream && frtSeconds > 0 {
		firstTokenTime = frtSeconds
	}
	if firstTokenTime > 0 {
		m.timedRequests++
		if firstTokenTime <= 5 {
			m.within5s++
		}
		if firstTokenTime <= 10 {
			m.within10s++
		}
	}

	if useTime > 0 {
		m.durationTimedRequests++
		if useTime <= 10 {
			m.durationWithin10s++
		}
		if useTime <= 20 {
			m.durationWithin20s++
		}
	}

	if completionTokens > 0 && useTime > 0 {
		effectiveTime := useTime
		if isStream && frtSeconds > 0 {
			effectiveTime = math.Max(useTime-frtSeconds, 1)
		}
		m.outputRequests++
		m.completionTokensSum += completionTokens
		m.useTimeSum += effectiveTime
	}

	cacheTokens := toFloat64(other["cache_tokens"])
	if cacheTokens > 0 {
		m.cacheTokensSum += cacheTokens
	}
	cacheWriteTokens := cacheWriteTokensFromOther(other)
	if cacheWriteTokens > 0 {
		m.cacheWriteTokensSum += cacheWriteTokens
	}
	isClaude := isClaudeMessagesRequest(other)
	if inputTokens := inputTokensFromLog(promptTokens, cacheTokens, cacheWriteTokens, isClaude, other); inputTokens > 0 {
		m.inputTokensSum += inputTokens
	}
	if completionTokens > 0 {
		m.outputTokensSum += completionTokens
	}
	if isClaude {
		// 与 SQL 路径保持一致: Claude 分母 = prompt + 缓存读 + 缓存写
		m.cacheDenominatorSum += promptTokens + cacheTokens + cacheWriteTokens
		m.cacheWriteSum += cacheWriteTokens
		m.claudeRequests++
	} else {
		m.cacheDenominatorSum += math.Max(promptTokens, cacheTokens)
	}

	return m
}

type availabilityStats struct {
	totalRequests int64
	successCount  int64
	failureCount  int64
	formatError   int64
	rateLimit     int64
	emptyCount    int64
	slots         map[int]*slotCounts
}

func newAvailabilityStats() *availabilityStats {
	return &availabilityStats{slots: make(map[int]*slotCounts)}
}

// accumulateMetrics 用预解析的 rowMetrics 累加可用性统计。
// 派生量已在 computeRowMetrics 里算好,这里只做廉价累加,不再触碰 other JSON。
func (s *availabilityStats) accumulateMetrics(m rowMetrics, startTime, slotSeconds int64, numSlots int) {
	if s == nil || !m.valid {
		return
	}

	slotIdx := int((m.createdAt - startTime) / slotSeconds)
	if slotIdx < 0 {
		slotIdx = 0
	}
	if slotIdx >= numSlots {
		slotIdx = numSlots - 1
	}

	counts := s.slots[slotIdx]
	if counts == nil {
		counts = &slotCounts{}
		s.slots[slotIdx] = counts
	}

	s.totalRequests++
	counts.total++

	switch {
	case m.isSuccess:
		s.successCount++
		counts.success++
	case m.isFailure:
		s.failureCount++
		counts.failure++
		switch m.errorBucket {
		case ErrorBucketUserFormat:
			s.formatError++
			counts.formatError++
		case ErrorBucketRateLimit:
			s.rateLimit++
			counts.rateLimit++
		}
	case m.isEmpty:
		s.emptyCount++
		counts.empty++
	}
	if m.isPerf {
		addSlotPerformanceMetrics(counts, m)
	}
}

// addSlotPerformanceMetrics 把 rowMetrics 的性能派生量累加进 slotCounts。
// 适用于实时与历史两条 slot 路径。
func addSlotPerformanceMetrics(slot *slotCounts, m rowMetrics) {
	if slot == nil {
		return
	}
	slot.timedRequests += m.timedRequests
	slot.within5s += m.within5s
	slot.within10s += m.within10s
	slot.durationTimedRequests += m.durationTimedRequests
	slot.durationWithin10s += m.durationWithin10s
	slot.durationWithin20s += m.durationWithin20s
	slot.outputRequests += m.outputRequests
	slot.claudeRequests += m.claudeRequests
	slot.cacheDenominatorSum += m.cacheDenominatorSum
	slot.cacheTokensSum += m.cacheTokensSum
	slot.cacheWriteSum += m.cacheWriteSum
	slot.cacheWriteTokensSum += m.cacheWriteTokensSum
	slot.inputTokensSum += m.inputTokensSum
	slot.outputTokensSum += m.outputTokensSum
	slot.completionTokensSum += m.completionTokensSum
	slot.useTimeSum += m.useTimeSum
}

// accumulatePerformanceMetrics 把 rowMetrics 累加进 performanceStats。
// successRequests 对所有 type==2 日志自增(即 m.isPerf),与可用性的 successCount 区分。
func accumulatePerformanceMetrics(stats *performanceStats, m rowMetrics) {
	if stats == nil || !m.isPerf {
		return
	}
	stats.successRequests++
	stats.timedRequests += m.timedRequests
	stats.within5s += m.within5s
	stats.within10s += m.within10s
	stats.durationTimedRequests += m.durationTimedRequests
	stats.durationWithin10s += m.durationWithin10s
	stats.durationWithin20s += m.durationWithin20s
	stats.outputRequests += m.outputRequests
	stats.claudeRequests += m.claudeRequests
	stats.cacheDenominatorSum += m.cacheDenominatorSum
	stats.cacheTokensSum += m.cacheTokensSum
	stats.cacheWriteSum += m.cacheWriteSum
	stats.cacheWriteTokensSum += m.cacheWriteTokensSum
	stats.inputTokensSum += m.inputTokensSum
	stats.outputTokensSum += m.outputTokensSum
	stats.completionTokensSum += m.completionTokensSum
	stats.useTimeSum += m.useTimeSum
}

// accumulateDailyMetrics 把 rowMetrics 的性能派生量累加进 dailyPerfStats。
// 只动性能累加器,可用性计数由 accumulateDayRow 负责。
func accumulateDailyMetrics(stats *dailyPerfStats, m rowMetrics) {
	if stats == nil || !m.isPerf {
		return
	}
	stats.timedRequests += m.timedRequests
	stats.within5s += m.within5s
	stats.within10s += m.within10s
	stats.durationTimedRequests += m.durationTimedRequests
	stats.durationWithin10s += m.durationWithin10s
	stats.durationWithin20s += m.durationWithin20s
	stats.outputRequests += m.outputRequests
	stats.claudeRequests += m.claudeRequests
	stats.cacheDenominatorSum += m.cacheDenominatorSum
	stats.cacheTokensSum += m.cacheTokensSum
	stats.cacheWriteSum += m.cacheWriteSum
	stats.cacheWriteTokensSum += m.cacheWriteTokensSum
	stats.inputTokensSum += m.inputTokensSum
	stats.outputTokensSum += m.outputTokensSum
	stats.completionTokensSum += m.completionTokensSum
	stats.useTimeSum += m.useTimeSum
}

func buildAvailabilitySlotData(slots map[int]*slotCounts, startTime int64, slotSeconds int64, numSlots int) []map[string]interface{} {
	slotData := make([]map[string]interface{}, 0, numSlots)
	rules := GetErrorRules()
	for i := 0; i < numSlots; i++ {
		slotStart := startTime + int64(i)*slotSeconds
		slotEnd := slotStart + slotSeconds
		c := slots[i]
		var total, success, failure, empty, formatError, rateLimit int64
		if c != nil {
			total, success, failure, empty = c.total, c.success, c.failure, c.empty
			formatError, rateLimit = c.formatError, c.rateLimit
		} else {
			c = &slotCounts{}
		}

		slotRate := float64(100)
		if total > 0 {
			slotRate = float64(success) / float64(total) * 100
		}
		modelRate := modelSuccessRate(success, total, formatError, rateLimit, rules)
		statusDenom := modelAvailabilityDenominator(total, formatError, rateLimit, rules)
		perf := buildPerformanceSummary(
			success,
			c.timedRequests,
			c.outputRequests,
			c.within5s,
			c.within10s,
			c.durationTimedRequests,
			c.durationWithin10s,
			c.durationWithin20s,
			c.claudeRequests,
			c.cacheDenominatorSum,
			c.cacheTokensSum,
			c.cacheWriteSum,
			c.cacheWriteTokensSum,
			c.inputTokensSum,
			c.outputTokensSum,
			c.completionTokensSum,
			c.useTimeSum,
		)

		slotData = append(slotData, map[string]interface{}{
			"slot":                     i,
			"start_time":               slotStart,
			"end_time":                 slotEnd,
			"total_requests":           total,
			"success_count":            success,
			"failure_count":            failure,
			"format_error_count":       formatError,
			"rate_limit_count":         rateLimit,
			"non_format_failure_count": nonFormatFailureCount(failure, formatError),
			"model_failure_count":      nonFormatFailureCount(failure, formatError),
			"model_error_count":        nonFormatFailureCount(failure, formatError) + empty,
			"non_empty_count":          success,
			"empty_count":              empty,
			"success_rate":             roundRate(slotRate),
			"model_success_rate":       roundRate(modelRate),
			"model_availability_rate":  roundRate(modelRate),
			"status":                   getStatusColor(slotRate, total),
			"model_status":             getStatusColor(modelRate, statusDenom),
			"within_5s_rate":           perf["within_5s_rate"],
			"within_10s_rate":          perf["within_10s_rate"],
			"duration_within_10s_rate": perf["duration_within_10s_rate"],
			"duration_within_20s_rate": perf["duration_within_20s_rate"],
			"cache_hit_rate":           perf["cache_hit_rate"],
			"cache_write_rate":         perf["cache_write_rate"],
			"cache_hit_tokens":         perf["cache_hit_tokens"],
			"cache_write_tokens":       perf["cache_write_tokens"],
			"total_input_tokens":       perf["total_input_tokens"],
			"total_output_tokens":      perf["total_output_tokens"],
			"completion_tps":           perf["completion_tps"],
			"timed_requests":           perf["timed_requests"],
			"duration_timed_requests":  perf["duration_timed_requests"],
			"output_requests":          perf["output_requests"],
		})
	}
	return slotData
}

// cacheWriteTokensFromOther mirrors cacheWriteTokensExpr / frontend
// getUsageLogCacheSummary: prefer normalized cache_write_tokens, then the
// 5m+1h split, then cache_creation_tokens.
func cacheWriteTokensFromOther(other map[string]interface{}) float64 {
	if v := toFloat64(other["cache_write_tokens"]); v > 0 {
		return v
	}
	split := toFloat64(other["cache_creation_tokens_5m"]) + toFloat64(other["cache_creation_tokens_1h"])
	creation := toFloat64(other["cache_creation_tokens"])
	if split > 0 {
		return math.Max(split, creation)
	}
	return creation
}

func (s *ModelStatusService) performanceLogColumns() string {
	columns := []string{
		"id",
		"created_at",
		"model_name",
		"channel_id",
		"type",
		"use_time",
		"prompt_tokens",
		"completion_tokens",
	}
	if s.db.ColumnExists("logs", "is_stream") {
		columns = append(columns, "is_stream")
	} else {
		columns = append(columns, "0 as is_stream")
	}
	if s.db.ColumnExists("logs", "other") {
		columns = append(columns, "other")
	} else {
		columns = append(columns, "NULL as other")
	}
	return strings.Join(columns, ", ")
}

func (s *ModelStatusService) fetchPerformanceLogBatch(startTime, now, cursorCreatedAt, cursorID int64, modelNames []string) ([]map[string]interface{}, error) {
	args := []interface{}{startTime, now, cursorCreatedAt, cursorCreatedAt, cursorID}
	modelFilter := ""
	if len(modelNames) > 0 {
		placeholders := make([]string, 0, len(modelNames))
		for _, modelName := range modelNames {
			placeholders = append(placeholders, "?")
			args = append(args, modelName)
		}
		modelFilter = fmt.Sprintf("AND model_name IN (%s)", strings.Join(placeholders, ", "))
	}
	args = append(args, performanceLogBatchSize)

	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT %s
		FROM logs
		WHERE created_at >= ? AND created_at < ?
			AND type IN (2, 5)
			AND (
				created_at > ?
				OR (created_at = ? AND id > ?)
			)
			%s
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, s.performanceLogColumns(), modelFilter))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

func (s *ModelStatusService) getChannelNameMap(channelIDs []int64) map[int64]string {
	result := make(map[int64]string)
	if len(channelIDs) == 0 {
		return result
	}

	seen := make(map[int64]bool, len(channelIDs))
	placeholders := make([]string, 0, len(channelIDs))
	args := make([]interface{}, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 || seen[channelID] {
			continue
		}
		seen[channelID] = true
		placeholders = append(placeholders, "?")
		args = append(args, channelID)
	}
	if len(args) == 0 {
		return result
	}

	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT id, COALESCE(name, '') as name
		FROM channels
		WHERE id IN (%s)`, strings.Join(placeholders, ", ")))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result
	}
	for _, row := range rows {
		channelID := toInt64(row["id"])
		name := toString(row["name"])
		if name == "" {
			name = fmt.Sprintf("Channel#%d", channelID)
		}
		result[channelID] = name
	}
	return result
}

func (s *ModelStatusService) getModelPerformanceMap(modelNames []string, startTime, now int64) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	if len(modelNames) == 0 {
		return result
	}

	statsByModel := make(map[string]*performanceStats, len(modelNames))
	for _, modelName := range modelNames {
		statsByModel[modelName] = &performanceStats{}
	}

	rules := GetErrorRules()
	cursorCreatedAt := startTime - 1
	cursorID := int64(0)
	for {
		rows, err := s.fetchPerformanceLogBatch(startTime, now, cursorCreatedAt, cursorID, modelNames)
		if err != nil || len(rows) == 0 {
			break
		}
		for _, row := range rows {
			modelName := toString(row["model_name"])
			if stats, ok := statsByModel[modelName]; ok {
				accumulatePerformanceMetrics(stats, computeRowMetrics(row, rules))
			}
		}
		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
	}

	for modelName, stats := range statsByModel {
		result[modelName] = performanceSummaryFromStats(stats)
	}

	return result
}

type realtimePerformanceAggregate struct {
	modelAvailability        map[string]*availabilityStats
	modelPerformance         map[string]*performanceStats
	channelAvailability      map[int64]*availabilityStats
	channelPerformance       map[int64]*performanceStats
	channelModelAvailability map[int64]map[string]*availabilityStats
	channelModelPerformance  map[int64]map[string]*performanceStats
}

func (s *ModelStatusService) aggregateRealtimePerformance(modelNames []string, startTime, now int64, slotSeconds int64, numSlots int) (*realtimePerformanceAggregate, error) {
	agg := &realtimePerformanceAggregate{
		modelAvailability:        make(map[string]*availabilityStats, len(modelNames)),
		modelPerformance:         make(map[string]*performanceStats, len(modelNames)),
		channelAvailability:      make(map[int64]*availabilityStats),
		channelPerformance:       make(map[int64]*performanceStats),
		channelModelAvailability: make(map[int64]map[string]*availabilityStats),
		channelModelPerformance:  make(map[int64]map[string]*performanceStats),
	}

	selectedModels := make(map[string]bool, len(modelNames))
	for _, modelName := range modelNames {
		selectedModels[modelName] = true
		if _, ok := agg.modelAvailability[modelName]; !ok {
			agg.modelAvailability[modelName] = newAvailabilityStats()
			agg.modelPerformance[modelName] = &performanceStats{}
		}
	}

	rules := GetErrorRules()
	cursorCreatedAt := startTime - 1
	cursorID := int64(0)
	for {
		rows, err := s.fetchPerformanceLogBatch(startTime, now, cursorCreatedAt, cursorID, nil)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			m := computeRowMetrics(row, rules)
			if !m.valid {
				continue
			}
			modelName := toString(row["model_name"])
			if selectedModels[modelName] {
				agg.modelAvailability[modelName].accumulateMetrics(m, startTime, slotSeconds, numSlots)
				accumulatePerformanceMetrics(agg.modelPerformance[modelName], m)
			}

			channelID := toInt64(row["channel_id"])
			channelAvailability := agg.channelAvailability[channelID]
			if channelAvailability == nil {
				channelAvailability = newAvailabilityStats()
				agg.channelAvailability[channelID] = channelAvailability
			}
			channelAvailability.accumulateMetrics(m, startTime, slotSeconds, numSlots)

			channelPerformance := agg.channelPerformance[channelID]
			if channelPerformance == nil {
				channelPerformance = &performanceStats{}
				agg.channelPerformance[channelID] = channelPerformance
			}
			accumulatePerformanceMetrics(channelPerformance, m)

			s.accumulateChannelModelRealtimeRow(agg.channelModelAvailability, agg.channelModelPerformance, row, m, startTime, slotSeconds, numSlots)
		}

		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
	}

	return agg, nil
}

func (s *ModelStatusService) accumulateChannelModelRealtimeRow(
	availabilityByChannelModel map[int64]map[string]*availabilityStats,
	statsByChannelModel map[int64]map[string]*performanceStats,
	row map[string]interface{},
	m rowMetrics,
	startTime int64,
	slotSeconds int64,
	numSlots int,
) {
	modelName := toString(row["model_name"])
	if modelName == "" {
		return
	}
	channelID := toInt64(row["channel_id"])

	modelAvailability := availabilityByChannelModel[channelID]
	if modelAvailability == nil {
		modelAvailability = make(map[string]*availabilityStats)
		availabilityByChannelModel[channelID] = modelAvailability
	}
	availability := modelAvailability[modelName]
	if availability == nil {
		availability = newAvailabilityStats()
		modelAvailability[modelName] = availability
	}
	availability.accumulateMetrics(m, startTime, slotSeconds, numSlots)

	modelStats := statsByChannelModel[channelID]
	if modelStats == nil {
		modelStats = make(map[string]*performanceStats)
		statsByChannelModel[channelID] = modelStats
	}
	stats := modelStats[modelName]
	if stats == nil {
		stats = &performanceStats{}
		modelStats[modelName] = stats
	}
	accumulatePerformanceMetrics(stats, m)
}

func buildModelPerformanceResult(modelName, window string, availability *availabilityStats, perfStats *performanceStats, startTime int64, slotSeconds int64, numSlots int, rules ErrorRuleConfig) map[string]interface{} {
	if availability == nil {
		availability = newAvailabilityStats()
	}
	perf := performanceSummaryFromStats(perfStats)

	successRate := float64(100)
	if availability.totalRequests > 0 {
		successRate = float64(availability.successCount) / float64(availability.totalRequests) * 100
	}
	modelRate := modelSuccessRate(availability.successCount, availability.totalRequests, availability.formatError, availability.rateLimit, rules)
	modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(availability.totalRequests, availability.formatError, availability.rateLimit, rules))

	return map[string]interface{}{
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              window,
		"total_requests":           availability.totalRequests,
		"success_count":            availability.successCount,
		"failure_count":            availability.failureCount,
		"format_error_count":       availability.formatError,
		"rate_limit_count":         availability.rateLimit,
		"non_format_failure_count": nonFormatFailureCount(availability.failureCount, availability.formatError),
		"model_failure_count":      nonFormatFailureCount(availability.failureCount, availability.formatError),
		"model_error_count":        nonFormatFailureCount(availability.failureCount, availability.formatError) + availability.emptyCount,
		"non_empty_count":          availability.successCount,
		"empty_count":              availability.emptyCount,
		"success_rate":             roundRate(successRate),
		"model_success_rate":       roundRate(modelRate),
		"model_availability_rate":  roundRate(modelRate),
		"current_status":           getStatusColor(successRate, availability.totalRequests),
		"model_current_status":     modelStatus,
		"within_5s_rate":           perf["within_5s_rate"],
		"within_10s_rate":          perf["within_10s_rate"],
		"duration_within_10s_rate": perf["duration_within_10s_rate"],
		"duration_within_20s_rate": perf["duration_within_20s_rate"],
		"cache_hit_rate":           perf["cache_hit_rate"],
		"cache_write_rate":         perf["cache_write_rate"],
		"cache_hit_tokens":         perf["cache_hit_tokens"],
		"cache_write_tokens":       perf["cache_write_tokens"],
		"total_input_tokens":       perf["total_input_tokens"],
		"total_output_tokens":      perf["total_output_tokens"],
		"completion_tps":           perf["completion_tps"],
		"timed_requests":           perf["timed_requests"],
		"duration_timed_requests":  perf["duration_timed_requests"],
		"output_requests":          perf["output_requests"],
		"slot_data":                buildAvailabilitySlotData(availability.slots, startTime, slotSeconds, numSlots),
	}
}

func buildChannelPerformanceResult(channelID int64, channelName string, availability *availabilityStats, perfStats *performanceStats, startTime int64, slotSeconds int64, numSlots int, rules ErrorRuleConfig) map[string]interface{} {
	if availability == nil {
		availability = newAvailabilityStats()
	}
	perf := performanceSummaryFromStats(perfStats)

	successRate := float64(100)
	if availability.totalRequests > 0 {
		successRate = float64(availability.successCount) / float64(availability.totalRequests) * 100
	}
	modelRate := modelSuccessRate(availability.successCount, availability.totalRequests, availability.formatError, availability.rateLimit, rules)
	modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(availability.totalRequests, availability.formatError, availability.rateLimit, rules))
	if channelName == "" {
		channelName = fmt.Sprintf("Channel#%d", channelID)
	}

	return map[string]interface{}{
		"channel_id":               channelID,
		"channel_name":             channelName,
		"total_requests":           availability.totalRequests,
		"success_count":            availability.successCount,
		"failure_count":            availability.failureCount,
		"format_error_count":       availability.formatError,
		"rate_limit_count":         availability.rateLimit,
		"non_format_failure_count": nonFormatFailureCount(availability.failureCount, availability.formatError),
		"model_failure_count":      nonFormatFailureCount(availability.failureCount, availability.formatError),
		"model_error_count":        nonFormatFailureCount(availability.failureCount, availability.formatError) + availability.emptyCount,
		"non_empty_count":          availability.successCount,
		"empty_count":              availability.emptyCount,
		"success_rate":             roundRate(successRate),
		"model_success_rate":       roundRate(modelRate),
		"model_availability_rate":  roundRate(modelRate),
		"current_status":           getStatusColor(successRate, availability.totalRequests),
		"model_current_status":     modelStatus,
		"within_5s_rate":           perf["within_5s_rate"],
		"within_10s_rate":          perf["within_10s_rate"],
		"duration_within_10s_rate": perf["duration_within_10s_rate"],
		"duration_within_20s_rate": perf["duration_within_20s_rate"],
		"cache_hit_rate":           perf["cache_hit_rate"],
		"cache_write_rate":         perf["cache_write_rate"],
		"cache_hit_tokens":         perf["cache_hit_tokens"],
		"cache_write_tokens":       perf["cache_write_tokens"],
		"total_input_tokens":       perf["total_input_tokens"],
		"total_output_tokens":      perf["total_output_tokens"],
		"completion_tps":           perf["completion_tps"],
		"timed_requests":           perf["timed_requests"],
		"duration_timed_requests":  perf["duration_timed_requests"],
		"output_requests":          perf["output_requests"],
		"slot_data":                buildAvailabilitySlotData(availability.slots, startTime, slotSeconds, numSlots),
	}
}

func channelModelDetailCacheKey(window string, channelID int64) string {
	return fmt.Sprintf("model_status:channel_model_detail:v2:%s:%d", window, channelID)
}

func buildChannelModelPerformanceDetails(
	channelID int64,
	channelName string,
	window string,
	availabilityByModel map[string]*availabilityStats,
	statsByModel map[string]*performanceStats,
	startTime int64,
	slotSeconds int64,
	numSlots int,
	rules ErrorRuleConfig,
) []map[string]interface{} {
	type modelStat struct {
		name         string
		availability *availabilityStats
		stats        *performanceStats
	}

	ordered := make([]modelStat, 0, len(availabilityByModel))
	for modelName, availability := range availabilityByModel {
		if modelName == "" || availability == nil || availability.totalRequests <= 0 {
			continue
		}
		stats := statsByModel[modelName]
		if stats == nil {
			stats = &performanceStats{}
		}
		ordered = append(ordered, modelStat{name: modelName, availability: availability, stats: stats})
	}

	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].availability.totalRequests == ordered[j].availability.totalRequests {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].availability.totalRequests > ordered[j].availability.totalRequests
	})

	results := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		model := buildModelPerformanceResult(item.name, window, item.availability, item.stats, startTime, slotSeconds, numSlots, rules)
		model["channel_id"] = channelID
		model["channel_name"] = channelName
		results = append(results, model)
	}
	return results
}

func (s *ModelStatusService) cacheChannelModelPerformanceDetails(
	window string,
	channelIDs []int64,
	channelNames map[int64]string,
	availabilityByChannelModel map[int64]map[string]*availabilityStats,
	statsByChannelModel map[int64]map[string]*performanceStats,
	startTime int64,
	slotSeconds int64,
	numSlots int,
	rules ErrorRuleConfig,
	ttl time.Duration,
) map[int64]int {
	cm := cache.Get()
	counts := make(map[int64]int, len(channelIDs))
	for _, channelID := range channelIDs {
		channelName := channelNames[channelID]
		if channelName == "" {
			channelName = fmt.Sprintf("Channel#%d", channelID)
		}
		details := buildChannelModelPerformanceDetails(
			channelID,
			channelName,
			window,
			availabilityByChannelModel[channelID],
			statsByChannelModel[channelID],
			startTime,
			slotSeconds,
			numSlots,
			rules,
		)
		counts[channelID] = len(details)
		_ = cm.Set(channelModelDetailCacheKey(window, channelID), details, ttl)
	}
	return counts
}

func performanceSummaryCacheKey(window string, modelNames []string) string {
	sum := sha1.Sum([]byte(strings.Join(modelNames, "\x00")))
	return fmt.Sprintf("model_status:performance_summary:v1:%s:%x", window, sum)
}

// GetRealtimePerformanceSummary scans the live logs once (in batches) and
// builds both model cards and channel cards from the same rows.
func (s *ModelStatusService) GetRealtimePerformanceSummary(modelNames []string, window string, useCache bool) (map[string]interface{}, error) {
	window = NormalizeTimeWindow(window)
	twConfig, ok := ParseTimeWindow(window)
	if !ok {
		window = DefaultTimeWindow
		twConfig = timeWindowConfigs[DefaultTimeWindow]
	}

	cacheKey := performanceSummaryCacheKey(window, modelNames)
	cm := cache.Get()
	if useCache {
		var cached map[string]interface{}
		found, _ := cm.GetJSON(cacheKey, &cached)
		if found {
			return cached, nil
		}
	}

	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	agg, err := s.aggregateRealtimePerformance(modelNames, startTime, now, twConfig.slotSeconds, twConfig.numSlots)
	if err != nil {
		return nil, err
	}

	rules := GetErrorRules()
	models := make([]map[string]interface{}, 0, len(modelNames))
	for _, modelName := range modelNames {
		models = append(models, buildModelPerformanceResult(
			modelName,
			window,
			agg.modelAvailability[modelName],
			agg.modelPerformance[modelName],
			startTime,
			twConfig.slotSeconds,
			twConfig.numSlots,
			rules,
		))
	}

	channelIDs := make([]int64, 0, len(agg.channelAvailability))
	for channelID := range agg.channelAvailability {
		channelIDs = append(channelIDs, channelID)
	}
	channelNames := s.getChannelNameMap(channelIDs)
	channelModelCounts := s.cacheChannelModelPerformanceDetails(
		window,
		channelIDs,
		channelNames,
		agg.channelModelAvailability,
		agg.channelModelPerformance,
		startTime,
		twConfig.slotSeconds,
		twConfig.numSlots,
		rules,
		modelStatusRealtimeCacheTTL,
	)

	sort.Slice(channelIDs, func(i, j int) bool {
		left := agg.channelAvailability[channelIDs[i]]
		right := agg.channelAvailability[channelIDs[j]]
		if left.totalRequests == right.totalRequests {
			return channelIDs[i] < channelIDs[j]
		}
		return left.totalRequests > right.totalRequests
	})

	channels := make([]map[string]interface{}, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		availability := agg.channelAvailability[channelID]
		if availability.totalRequests <= 0 {
			continue
		}
		channel := buildChannelPerformanceResult(
			channelID,
			channelNames[channelID],
			availability,
			agg.channelPerformance[channelID],
			startTime,
			twConfig.slotSeconds,
			twConfig.numSlots,
			rules,
		)
		channel["model_count"] = channelModelCounts[channelID]
		channels = append(channels, channel)
	}

	result := map[string]interface{}{
		"models":      models,
		"channels":    channels,
		"time_window": window,
	}
	cm.Set(cacheKey, result, modelStatusRealtimeCacheTTL)
	return result, nil
}

func (s *ModelStatusService) GetChannelPerformanceSummaries(window string, useCache ...bool) ([]map[string]interface{}, error) {
	window = NormalizeTimeWindow(window)
	useCachedResult := true
	if len(useCache) > 0 {
		useCachedResult = useCache[0]
	}
	cacheKey := fmt.Sprintf("model_status:channel_performance:v2:%s", window)
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if useCachedResult && found {
		return cached, nil
	}

	twConfig, ok := ParseTimeWindow(window)
	if !ok {
		window = DefaultTimeWindow
		twConfig = timeWindowConfigs[DefaultTimeWindow]
	}

	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	numSlots := twConfig.numSlots
	slotSeconds := twConfig.slotSeconds

	rules := GetErrorRules()
	statsByChannel := make(map[int64]*performanceStats)
	availabilityByChannel := make(map[int64]*availabilityStats)
	statsByChannelModel := make(map[int64]map[string]*performanceStats)
	availabilityByChannelModel := make(map[int64]map[string]*availabilityStats)
	cursorCreatedAt := startTime - 1
	cursorID := int64(0)
	for {
		rows, err := s.fetchPerformanceLogBatch(startTime, now, cursorCreatedAt, cursorID, nil)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			m := computeRowMetrics(row, rules)
			if !m.valid {
				continue
			}
			channelID := toInt64(row["channel_id"])
			availability := availabilityByChannel[channelID]
			if availability == nil {
				availability = newAvailabilityStats()
				availabilityByChannel[channelID] = availability
			}
			availability.accumulateMetrics(m, startTime, slotSeconds, numSlots)

			stats := statsByChannel[channelID]
			if stats == nil {
				stats = &performanceStats{}
				statsByChannel[channelID] = stats
			}
			accumulatePerformanceMetrics(stats, m)

			s.accumulateChannelModelRealtimeRow(availabilityByChannelModel, statsByChannelModel, row, m, startTime, slotSeconds, numSlots)
		}
		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
	}

	channelIDs := make([]int64, 0, len(availabilityByChannel))
	for channelID := range availabilityByChannel {
		channelIDs = append(channelIDs, channelID)
	}
	channelNames := s.getChannelNameMap(channelIDs)
	channelModelCounts := s.cacheChannelModelPerformanceDetails(
		window,
		channelIDs,
		channelNames,
		availabilityByChannelModel,
		statsByChannelModel,
		startTime,
		slotSeconds,
		numSlots,
		rules,
		modelStatusRealtimeCacheTTL,
	)

	type channelStat struct {
		id           int64
		stats        *performanceStats
		availability *availabilityStats
	}
	ordered := make([]channelStat, 0, len(availabilityByChannel))
	for channelID, availability := range availabilityByChannel {
		if availability.totalRequests > 0 {
			stats := statsByChannel[channelID]
			if stats == nil {
				stats = &performanceStats{}
			}
			ordered = append(ordered, channelStat{id: channelID, stats: stats, availability: availability})
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].availability.totalRequests == ordered[j].availability.totalRequests {
			return ordered[i].id < ordered[j].id
		}
		return ordered[i].availability.totalRequests > ordered[j].availability.totalRequests
	})

	results := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		perf := performanceSummaryFromStats(item.stats)
		availability := item.availability
		successRate := float64(100)
		if availability.totalRequests > 0 {
			successRate = float64(availability.successCount) / float64(availability.totalRequests) * 100
		}
		modelRate := modelSuccessRate(availability.successCount, availability.totalRequests, availability.formatError, availability.rateLimit, rules)
		modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(availability.totalRequests, availability.formatError, availability.rateLimit, rules))
		channelName := channelNames[item.id]
		if channelName == "" {
			channelName = fmt.Sprintf("Channel#%d", item.id)
		}
		results = append(results, map[string]interface{}{
			"channel_id":               item.id,
			"channel_name":             channelName,
			"model_count":              channelModelCounts[item.id],
			"total_requests":           availability.totalRequests,
			"success_count":            availability.successCount,
			"failure_count":            availability.failureCount,
			"format_error_count":       availability.formatError,
			"rate_limit_count":         availability.rateLimit,
			"non_format_failure_count": nonFormatFailureCount(availability.failureCount, availability.formatError),
			"model_failure_count":      nonFormatFailureCount(availability.failureCount, availability.formatError),
			"model_error_count":        nonFormatFailureCount(availability.failureCount, availability.formatError) + availability.emptyCount,
			"non_empty_count":          availability.successCount,
			"empty_count":              availability.emptyCount,
			"success_rate":             roundRate(successRate),
			"model_success_rate":       roundRate(modelRate),
			"model_availability_rate":  roundRate(modelRate),
			"current_status":           getStatusColor(successRate, availability.totalRequests),
			"model_current_status":     modelStatus,
			"within_5s_rate":           perf["within_5s_rate"],
			"within_10s_rate":          perf["within_10s_rate"],
			"duration_within_10s_rate": perf["duration_within_10s_rate"],
			"duration_within_20s_rate": perf["duration_within_20s_rate"],
			"cache_hit_rate":           perf["cache_hit_rate"],
			"cache_write_rate":         perf["cache_write_rate"],
			"cache_hit_tokens":         perf["cache_hit_tokens"],
			"cache_write_tokens":       perf["cache_write_tokens"],
			"total_input_tokens":       perf["total_input_tokens"],
			"total_output_tokens":      perf["total_output_tokens"],
			"completion_tps":           perf["completion_tps"],
			"timed_requests":           perf["timed_requests"],
			"duration_timed_requests":  perf["duration_timed_requests"],
			"output_requests":          perf["output_requests"],
			"slot_data":                buildAvailabilitySlotData(availability.slots, startTime, slotSeconds, numSlots),
		})
	}

	cm.Set(cacheKey, results, modelStatusRealtimeCacheTTL)
	return results, nil
}

func (s *ModelStatusService) GetChannelModelPerformance(channelID int64, window string, limit, offset int) (map[string]interface{}, error) {
	window = NormalizeTimeWindow(window)
	if _, ok := ParseTimeWindow(window); !ok {
		window = DefaultTimeWindow
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	cm := cache.Get()
	cacheKey := channelModelDetailCacheKey(window, channelID)
	all := make([]map[string]interface{}, 0)
	found, _ := cm.GetJSON(cacheKey, &all)
	if !found {
		if _, err := s.GetChannelPerformanceSummaries(window, false); err != nil {
			return nil, err
		}
		found, _ = cm.GetJSON(cacheKey, &all)
		if !found {
			all = make([]map[string]interface{}, 0)
		}
	}

	total := len(all)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	data := all[start:end]

	channelName := ""
	if len(all) > 0 {
		channelName = toString(all[0]["channel_name"])
	}
	if channelName == "" {
		names := s.getChannelNameMap([]int64{channelID})
		channelName = names[channelID]
	}
	if channelName == "" {
		channelName = fmt.Sprintf("Channel#%d", channelID)
	}

	return map[string]interface{}{
		"channel_id":   channelID,
		"channel_name": channelName,
		"window":       window,
		"time_window":  window,
		"total":        total,
		"limit":        limit,
		"offset":       offset,
		"has_more":    offset+len(data) < total,
		"data":         data,
	}, nil
}

// GetAvailableModels returns all models with 24h request counts
func (s *ModelStatusService) GetAvailableModels() ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("model_status:available_models", &cached)
	if found {
		return cached, nil
	}

	startTime := time.Now().Unix() - 86400

	query := s.db.RebindQuery(`
		SELECT model_name, COUNT(*) as request_count_24h
		FROM logs
		WHERE type IN (2, 5) AND model_name != '' AND created_at >= ?
		GROUP BY model_name
		ORDER BY request_count_24h DESC`)

	rows, err := s.db.Query(query, startTime)
	if err != nil {
		return nil, err
	}

	cm.Set("model_status:available_models", rows, 5*time.Minute)
	return rows, nil
}

// GetModelStatus returns status for a specific model
// Uses a single GROUP BY FLOOR query (matches Python backend optimization)
func (s *ModelStatusService) GetModelStatus(modelName, window string) (map[string]interface{}, error) {
	cacheKey := fmt.Sprintf("model_status:v2:%s:%s", modelName, window)
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	// Get window configuration (dynamic slot count per window)
	twConfig, ok := ParseTimeWindow(window)
	if !ok {
		twConfig = timeWindowConfigs["24h"]
	}

	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	numSlots := twConfig.numSlots
	slotSeconds := twConfig.slotSeconds
	rules := GetErrorRules()

	// Single optimized query — aggregate by time slot using FLOOR division
	// This reduces N queries to 1 query per model (matches Python backend)
	//
	// Success counting strategy:
	//   - type=2 with completion_tokens > 0 → definite success
	//   - type=2 with completion_tokens = 0 → empty response (likely failure)
	//   - type=5 → explicit failure (if NewAPI version supports it)
	// This ensures correct success rate even when NewAPI doesn't log type=5 failures.
	slotQuery := s.db.RebindQuery(fmt.Sprintf(`
		SELECT FLOOR((created_at - %d) / %d) as slot_idx,
			COUNT(*) as total,
			SUM(CASE WHEN type = 2 AND completion_tokens > 0 THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure,
			SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty,
			SUM(CASE WHEN type = 5 AND %s THEN 1 ELSE 0 END) as format_error,
			SUM(CASE WHEN type = 5 AND %s THEN 1 ELSE 0 END) as rate_limit
		FROM logs
		WHERE model_name = ?
			AND created_at >= ? AND created_at < ?
			AND type IN (2, 5)
		GROUP BY FLOOR((created_at - %d) / %d)`,
		startTime, slotSeconds,
		s.userFormatErrorSQLCondition("other", rules),
		s.rateLimitErrorSQLCondition("other", rules),
		startTime, slotSeconds))

	rows, _ := s.db.Query(slotQuery, modelName, startTime, now)

	// Initialize all slots with zeros
	type slotInfo struct {
		total   int64
		success int64
		failure int64
		format  int64
		rateLim int64
		empty   int64
	}
	slotMap := make(map[int64]*slotInfo, numSlots)

	// Fill in actual data from query results
	if rows != nil {
		for _, row := range rows {
			idx := toInt64(row["slot_idx"])
			if idx >= 0 && idx < int64(numSlots) {
				slotMap[idx] = &slotInfo{
					total:   toInt64(row["total"]),
					success: toInt64(row["success"]),
					failure: toInt64(row["failure"]),
					format:  toInt64(row["format_error"]),
					rateLim: toInt64(row["rate_limit"]),
					empty:   toInt64(row["empty"]),
				}
			}
		}
	}

	// Build slot_data list with status colors
	slotData := make([]map[string]interface{}, 0, numSlots)
	totalReqs := int64(0)
	totalSuccess := int64(0)
	totalFailure := int64(0)
	totalFormatError := int64(0)
	totalRateLimit := int64(0)
	totalEmpty := int64(0)

	for i := 0; i < numSlots; i++ {
		slotStart := startTime + int64(i)*slotSeconds
		slotEnd := slotStart + slotSeconds

		si := slotMap[int64(i)]
		slotTotal := int64(0)
		slotSuccess := int64(0)
		slotFailure := int64(0)
		slotFormatError := int64(0)
		slotRateLimit := int64(0)
		slotEmpty := int64(0)
		if si != nil {
			slotTotal = si.total
			slotSuccess = si.success
			slotFailure = si.failure
			slotFormatError = si.format
			slotRateLimit = si.rateLim
			slotEmpty = si.empty
		}

		slotRate := float64(100)
		if slotTotal > 0 {
			slotRate = float64(slotSuccess) / float64(slotTotal) * 100
		}
		slotModelRate := modelSuccessRate(slotSuccess, slotTotal, slotFormatError, slotRateLimit, rules)
		slotStatusDenom := modelAvailabilityDenominator(slotTotal, slotFormatError, slotRateLimit, rules)

		slotData = append(slotData, map[string]interface{}{
			"slot":                     i,
			"start_time":               slotStart,
			"end_time":                 slotEnd,
			"total_requests":           slotTotal,
			"success_count":            slotSuccess,
			"failure_count":            slotFailure,
			"format_error_count":       slotFormatError,
			"rate_limit_count":         slotRateLimit,
			"non_format_failure_count": nonFormatFailureCount(slotFailure, slotFormatError),
			"model_failure_count":      nonFormatFailureCount(slotFailure, slotFormatError),
			"model_error_count":        nonFormatFailureCount(slotFailure, slotFormatError) + slotEmpty,
			"non_empty_count":          slotSuccess,
			"empty_count":              slotEmpty,
			"success_rate":             roundRate(slotRate),
			"model_success_rate":       roundRate(slotModelRate),
			"model_availability_rate":  roundRate(slotModelRate),
			"status":                   getStatusColor(slotRate, slotTotal),
			"model_status":             getStatusColor(slotModelRate, slotStatusDenom),
		})

		totalReqs += slotTotal
		totalSuccess += slotSuccess
		totalFailure += slotFailure
		totalFormatError += slotFormatError
		totalRateLimit += slotRateLimit
		totalEmpty += slotEmpty
	}

	overallRate := float64(100)
	if totalReqs > 0 {
		overallRate = float64(totalSuccess) / float64(totalReqs) * 100
	}
	modelRate := modelSuccessRate(totalSuccess, totalReqs, totalFormatError, totalRateLimit, rules)
	modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(totalReqs, totalFormatError, totalRateLimit, rules))

	perf := s.getModelPerformanceMap([]string{modelName}, startTime, now)[modelName]

	result := map[string]interface{}{
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              window,
		"total_requests":           totalReqs,
		"success_count":            totalSuccess,
		"failure_count":            totalFailure,
		"format_error_count":       totalFormatError,
		"rate_limit_count":         totalRateLimit,
		"non_format_failure_count": nonFormatFailureCount(totalFailure, totalFormatError),
		"model_failure_count":      nonFormatFailureCount(totalFailure, totalFormatError),
		"model_error_count":        nonFormatFailureCount(totalFailure, totalFormatError) + totalEmpty,
		"non_empty_count":          totalSuccess,
		"empty_count":              totalEmpty,
		"success_rate":             roundRate(overallRate),
		"model_success_rate":       roundRate(modelRate),
		"model_availability_rate":  roundRate(modelRate),
		"current_status":           getStatusColor(overallRate, totalReqs),
		"model_current_status":     modelStatus,
		"within_5s_rate":           perf["within_5s_rate"],
		"within_10s_rate":          perf["within_10s_rate"],
		"duration_within_10s_rate": perf["duration_within_10s_rate"],
		"duration_within_20s_rate": perf["duration_within_20s_rate"],
		"cache_hit_rate":           perf["cache_hit_rate"],
		"cache_write_rate":         perf["cache_write_rate"],
		"cache_hit_tokens":         perf["cache_hit_tokens"],
		"cache_write_tokens":       perf["cache_write_tokens"],
		"total_input_tokens":       perf["total_input_tokens"],
		"total_output_tokens":      perf["total_output_tokens"],
		"completion_tps":           perf["completion_tps"],
		"timed_requests":           perf["timed_requests"],
		"duration_timed_requests":  perf["duration_timed_requests"],
		"output_requests":          perf["output_requests"],
		"slot_data":                slotData,
	}

	cm.Set(cacheKey, result, 30*time.Second)
	return result, nil
}

// GetMultipleModelsStatus returns status for multiple models
func (s *ModelStatusService) GetMultipleModelsStatus(modelNames []string, window string) ([]map[string]interface{}, error) {
	results := make([]map[string]interface{}, 0, len(modelNames))
	twConfig, ok := ParseTimeWindow(window)
	if !ok {
		twConfig = timeWindowConfigs["24h"]
	}
	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	perfMap := s.getModelPerformanceMap(modelNames, startTime, now)
	for _, name := range modelNames {
		status, err := s.GetModelStatus(name, window)
		if err != nil {
			continue
		}
		if perf, ok := perfMap[name]; ok {
			status["within_5s_rate"] = perf["within_5s_rate"]
			status["within_10s_rate"] = perf["within_10s_rate"]
			status["duration_within_10s_rate"] = perf["duration_within_10s_rate"]
			status["duration_within_20s_rate"] = perf["duration_within_20s_rate"]
			status["cache_hit_rate"] = perf["cache_hit_rate"]
			status["cache_write_rate"] = perf["cache_write_rate"]
			status["cache_hit_tokens"] = perf["cache_hit_tokens"]
			status["cache_write_tokens"] = perf["cache_write_tokens"]
			status["total_input_tokens"] = perf["total_input_tokens"]
			status["total_output_tokens"] = perf["total_output_tokens"]
			status["completion_tps"] = perf["completion_tps"]
			status["timed_requests"] = perf["timed_requests"]
			status["duration_timed_requests"] = perf["duration_timed_requests"]
			status["output_requests"] = perf["output_requests"]
		}
		results = append(results, status)
	}
	return results, nil
}

// GetAllModelsStatus returns status for all models that have requests
func (s *ModelStatusService) GetAllModelsStatus(window string) ([]map[string]interface{}, error) {
	models, err := s.GetAvailableModels()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(models))
	for _, m := range models {
		if name, ok := m["model_name"].(string); ok {
			names = append(names, name)
		}
	}

	return s.GetMultipleModelsStatus(names, window)
}

// GetTokenGroups 返回令牌分组列表及其关联的模型（基于 abilities 表）
func (s *ModelStatusService) GetTokenGroups() ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("model_status:token_groups", &cached)
	if found {
		return cached, nil
	}

	// 从 abilities 表获取分组及其模型列表（abilities 表定义了 group-model-channel 的映射）
	groupCol := s.getGroupCol()
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT COALESCE(NULLIF(a.%s, ''), 'default') as group_name,
			COUNT(DISTINCT a.model) as model_count
		FROM abilities a
		INNER JOIN channels c ON c.id = a.channel_id
		WHERE c.status = 1
		GROUP BY COALESCE(NULLIF(a.%s, ''), 'default')
		ORDER BY model_count DESC`, groupCol, groupCol))

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}

	// 为每个分组获取其模型列表
	results := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		groupName := fmt.Sprintf("%v", row["group_name"])

		modelsQuery := s.db.RebindQuery(fmt.Sprintf(`
			SELECT DISTINCT a.model as model_name
			FROM abilities a
			INNER JOIN channels c ON c.id = a.channel_id
			WHERE c.status = 1 AND COALESCE(NULLIF(a.%s, ''), 'default') = ?
			ORDER BY a.model`, groupCol))

		modelRows, err := s.db.Query(modelsQuery, groupName)
		if err != nil {
			continue
		}

		modelNames := make([]string, 0, len(modelRows))
		for _, mr := range modelRows {
			if name, ok := mr["model_name"].(string); ok && name != "" {
				modelNames = append(modelNames, name)
			}
		}

		results = append(results, map[string]interface{}{
			"group_name":  groupName,
			"model_count": row["model_count"],
			"models":      modelNames,
		})
	}

	cm.Set("model_status:token_groups", results, 5*time.Minute)
	return results, nil
}

// getGroupCol 返回正确引用的 group 列名（group 是保留字）
func (s *ModelStatusService) getGroupCol() string {
	if s.db.IsPG {
		return `"group"`
	}
	return "`group`"
}

// Config management via cache

// GetSelectedModels returns selected model names from cache
func (s *ModelStatusService) GetSelectedModels() []string {
	cm := cache.Get()
	var models []string
	found, _ := cm.GetJSON("model_status:selected_models", &models)
	if found {
		return models
	}
	return []string{}
}

// SetSelectedModels saves selected models to cache
func (s *ModelStatusService) SetSelectedModels(models []string) {
	cm := cache.Get()
	cm.Set("model_status:selected_models", models, 0) // no expiry
}

// GetConfig returns all model status config
func (s *ModelStatusService) GetConfig() map[string]interface{} {
	cm := cache.Get()

	var timeWindow string
	found, _ := cm.GetJSON("model_status:time_window", &timeWindow)
	if !found {
		timeWindow = DefaultTimeWindow
	}

	var theme string
	found, _ = cm.GetJSON("model_status:theme", &theme)
	if !found {
		theme = DefaultTheme
	}
	// Map legacy theme names to valid ones
	if mapped, ok := LegacyThemeMap[theme]; ok {
		theme = mapped
	}

	var refreshInterval int
	found, _ = cm.GetJSON("model_status:refresh_interval", &refreshInterval)
	if !found {
		refreshInterval = 60
	}

	var sortMode string
	found, _ = cm.GetJSON("model_status:sort_mode", &sortMode)
	if !found {
		sortMode = "default"
	}

	var customOrder []string
	cm.GetJSON("model_status:custom_order", &customOrder)

	var customGroups []map[string]interface{}
	found, _ = cm.GetJSON("model_status:custom_groups", &customGroups)
	if !found {
		customGroups = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"time_window":      timeWindow,
		"theme":            theme,
		"refresh_interval": refreshInterval,
		"sort_mode":        sortMode,
		"custom_order":     customOrder,
		"selected_models":  s.GetSelectedModels(),
		"custom_groups":    customGroups,
		"site_title":       s.GetSiteTitle(),
		"error_rules":      GetErrorRules(),
	}
}

// SetTimeWindow saves time window to cache
func (s *ModelStatusService) SetTimeWindow(window string) {
	cm := cache.Get()
	cm.Set("model_status:time_window", NormalizeTimeWindow(window), 0)
}

// SetTheme saves theme to cache
func (s *ModelStatusService) SetTheme(theme string) {
	cm := cache.Get()
	cm.Set("model_status:theme", theme, 0)
}

// SetRefreshInterval saves refresh interval to cache
func (s *ModelStatusService) SetRefreshInterval(interval int) {
	cm := cache.Get()
	cm.Set("model_status:refresh_interval", interval, 0)
}

// SetSortMode saves sort mode to cache
func (s *ModelStatusService) SetSortMode(mode string) {
	cm := cache.Get()
	cm.Set("model_status:sort_mode", mode, 0)
}

// SetCustomOrder saves custom order to cache
func (s *ModelStatusService) SetCustomOrder(order []string) {
	cm := cache.Get()
	cm.Set("model_status:custom_order", order, 0)
}

// GetCustomGroups returns custom model groups from cache
func (s *ModelStatusService) GetCustomGroups() []map[string]interface{} {
	cm := cache.Get()
	var groups []map[string]interface{}
	found, _ := cm.GetJSON("model_status:custom_groups", &groups)
	if found {
		return groups
	}
	return []map[string]interface{}{}
}

// SetCustomGroups saves custom model groups to cache
func (s *ModelStatusService) SetCustomGroups(groups []map[string]interface{}) {
	cm := cache.Get()
	cm.Set("model_status:custom_groups", groups, 0) // no expiry
}

// GetSiteTitle returns the custom site title
func (s *ModelStatusService) GetSiteTitle() string {
	cm := cache.Get()
	var title string
	found, _ := cm.GetJSON("model_status:site_title", &title)
	if found {
		return title
	}
	return ""
}

// SetSiteTitle saves the custom site title
func (s *ModelStatusService) SetSiteTitle(title string) {
	cm := cache.Get()
	cm.Set("model_status:site_title", title, 0)
}

// GetEmbedConfig returns embed page configuration
func (s *ModelStatusService) GetEmbedConfig() map[string]interface{} {
	config := s.GetConfig()
	config["available_time_windows"] = AvailableTimeWindows
	config["available_themes"] = AvailableThemes
	config["available_refresh_intervals"] = AvailableRefreshIntervals
	config["available_sort_modes"] = AvailableSortModes
	return config
}

// AggregateDay scans the main logs table for the given local-day window
// [date 00:00, next day 00:00) once, building per-model availability slots +
// performance accumulators and per-channel performance accumulators, then
// persists them into the SQLite history store. It reuses the same batch-scan
// and accumulation helpers as the live path so metric semantics stay identical.
//
// date must be in YYYY-MM-DD form (interpreted in the server's local timezone).
func (s *ModelStatusService) AggregateDay(date string) error {
	hist, err := GetModelHistoryService()
	if err != nil {
		return err
	}

	dayStart, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return fmt.Errorf("invalid date %q: %w", date, err)
	}
	startTime := dayStart.Unix()
	endTime := dayStart.AddDate(0, 0, 1).Unix()

	snap := &daySnapshot{
		date:          date,
		startTS:       startTime,
		models:        make(map[string]*dailyPerfStats),
		slots:         make(map[string]map[int]*slotCounts),
		channels:      make(map[int64]*dailyPerfStats),
		chanSlot:      make(map[int64]map[int]*slotCounts),
		chanName:      make(map[int64]string),
		channelModels: make(map[int64]map[string]*dailyPerfStats),
		chanModelSlot: make(map[int64]map[string]map[int]*slotCounts),
	}
	rules := GetErrorRules()

	cursorCreatedAt := startTime - 1
	cursorID := int64(0)
	for {
		rows, err := s.fetchPerformanceLogBatch(startTime, endTime, cursorCreatedAt, cursorID, nil)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			s.accumulateDayRow(snap, row, startTime, rules)
		}
		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
		time.Sleep(modelHistoryBatchInterval)
	}

	// Resolve channel display names for the channels we saw.
	channelIDs := make([]int64, 0, len(snap.channels))
	for id := range snap.channels {
		channelIDs = append(channelIDs, id)
	}
	names := s.getChannelNameMap(channelIDs)
	for id := range snap.channels {
		if name, ok := names[id]; ok {
			snap.chanName[id] = name
		}
	}

	return hist.SaveDay(snap)
}

// accumulateDayRow folds a single log row into both the per-model and
// per-channel accumulators of the snapshot.
func (s *ModelStatusService) accumulateDayRow(snap *daySnapshot, row map[string]interface{}, startTime int64, rules ErrorRuleConfig) {
	m := computeRowMetrics(row, rules)
	if !m.valid {
		return
	}
	modelName := toString(row["model_name"])
	channelID := toInt64(row["channel_id"])
	slotIdx := int((m.createdAt - startTime) / historySlotSeconds)
	if slotIdx < 0 {
		slotIdx = 0
	}
	if slotIdx >= historySlotCount {
		slotIdx = historySlotCount - 1
	}

	// --- Availability slot counts (hourly) for the model ---
	if modelName != "" {
		modelSlots := snap.slots[modelName]
		if modelSlots == nil {
			modelSlots = make(map[int]*slotCounts)
			snap.slots[modelName] = modelSlots
		}
		c := modelSlots[slotIdx]
		if c == nil {
			c = &slotCounts{}
			modelSlots[slotIdx] = c
		}
		c.total++

		mstat := snap.models[modelName]
		if mstat == nil {
			mstat = &dailyPerfStats{}
			snap.models[modelName] = mstat
		}
		mstat.totalRequests++

		switch {
		case m.isSuccess:
			c.success++
			mstat.successCount++
		case m.isFailure:
			c.failure++
			mstat.failureCount++
			switch m.errorBucket {
			case ErrorBucketUserFormat:
				c.formatError++
				mstat.formatError++
			case ErrorBucketRateLimit:
				c.rateLimit++
				mstat.rateLimit++
			}
		case m.isEmpty:
			c.empty++
			mstat.emptyCount++
		}
	}

	var chanModelStat *dailyPerfStats
	var chanModelSlot *slotCounts
	if modelName != "" {
		channelModels := snap.channelModels[channelID]
		if channelModels == nil {
			channelModels = make(map[string]*dailyPerfStats)
			snap.channelModels[channelID] = channelModels
		}
		chanModelStat = channelModels[modelName]
		if chanModelStat == nil {
			chanModelStat = &dailyPerfStats{}
			channelModels[modelName] = chanModelStat
		}

		channelModelSlots := snap.chanModelSlot[channelID]
		if channelModelSlots == nil {
			channelModelSlots = make(map[string]map[int]*slotCounts)
			snap.chanModelSlot[channelID] = channelModelSlots
		}
		modelSlots := channelModelSlots[modelName]
		if modelSlots == nil {
			modelSlots = make(map[int]*slotCounts)
			channelModelSlots[modelName] = modelSlots
		}
		chanModelSlot = modelSlots[slotIdx]
		if chanModelSlot == nil {
			chanModelSlot = &slotCounts{}
			modelSlots[slotIdx] = chanModelSlot
		}

		chanModelStat.totalRequests++
		chanModelSlot.total++
		switch {
		case m.isSuccess:
			chanModelStat.successCount++
			chanModelSlot.success++
		case m.isFailure:
			chanModelStat.failureCount++
			chanModelSlot.failure++
			switch m.errorBucket {
			case ErrorBucketUserFormat:
				chanModelStat.formatError++
				chanModelSlot.formatError++
			case ErrorBucketRateLimit:
				chanModelStat.rateLimit++
				chanModelSlot.rateLimit++
			}
		case m.isEmpty:
			chanModelStat.emptyCount++
			chanModelSlot.empty++
		}
	}

	chStat := snap.channels[channelID]
	if chStat == nil {
		chStat = &dailyPerfStats{}
		snap.channels[channelID] = chStat
	}
	channelSlots := snap.chanSlot[channelID]
	if channelSlots == nil {
		channelSlots = make(map[int]*slotCounts)
		snap.chanSlot[channelID] = channelSlots
	}
	chSlot := channelSlots[slotIdx]
	if chSlot == nil {
		chSlot = &slotCounts{}
		channelSlots[slotIdx] = chSlot
	}
	chSlot.total++
	chStat.totalRequests++
	switch {
	case m.isSuccess:
		chStat.successCount++
		chSlot.success++
	case m.isFailure:
		chStat.failureCount++
		chSlot.failure++
		switch m.errorBucket {
		case ErrorBucketUserFormat:
			chStat.formatError++
			chSlot.formatError++
		case ErrorBucketRateLimit:
			chStat.rateLimit++
			chSlot.rateLimit++
		}
	case m.isEmpty:
		chStat.emptyCount++
		chSlot.empty++
	}

	// --- Performance accumulators (type=2 only, mirrors live path) ---
	if !m.isPerf {
		return
	}
	if modelName != "" {
		accumulateDailyMetrics(snap.models[modelName], m)
		addSlotPerformanceMetrics(snap.slots[modelName][slotIdx], m)
		accumulateDailyMetrics(chanModelStat, m)
		addSlotPerformanceMetrics(chanModelSlot, m)
	}
	accumulateDailyMetrics(chStat, m)
	addSlotPerformanceMetrics(chSlot, m)
}
