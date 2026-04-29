package service

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
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
)

// Time window slot configurations: {totalSeconds, numSlots, slotSeconds}
// Must match Python backend and frontend TIME_WINDOWS exactly
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

func buildPerformanceSummary(totalRequests, timedRequests, outputRequests, within5s, within10s, durationTimedRequests, durationWithin10s, durationWithin20s int64, cacheDenominatorSum, cacheTokensSum, completionTokensSum, useTimeSum float64) map[string]interface{} {
	var within5sRate interface{}
	var within10sRate interface{}
	var durationWithin10sRate interface{}
	var durationWithin20sRate interface{}
	var cacheHitRate interface{}
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
	requestPathExpr := s.requestPathExpr(otherCol)
	requestConversionExpr := s.requestConversionExpr(otherCol)
	isClaudeExpr := s.jsonBoolExpr(otherCol, "claude")
	return fmt.Sprintf(`COALESCE(SUM(CASE
		WHEN %s = 2
		THEN CASE
			WHEN %s
				OR %s LIKE '%%-> claude messages%%'
				OR %s LIKE '%%->claude messages%%'
				OR %s = 'claude messages'
				OR (%s = '' AND %s LIKE '%%/v1/messages%%')
			THEN COALESCE(%s, 0) + COALESCE((%s), 0)
			ELSE GREATEST(COALESCE(%s, 0), COALESCE((%s), 0))
		END
		ELSE 0
	END), 0) as cache_denominator_sum`, typeCol, isClaudeExpr, requestConversionExpr, requestConversionExpr, requestConversionExpr, requestConversionExpr, requestPathExpr, promptCol, cacheTokensExpr, promptCol, cacheTokensExpr)
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
		stats.cacheDenominatorSum,
		stats.cacheTokensSum,
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

func accumulatePerformanceRow(stats *performanceStats, row map[string]interface{}) {
	if stats == nil || toInt64(row["type"]) != 2 {
		return
	}

	stats.successRequests++

	useTime := toFloat64(row["use_time"])
	promptTokens := toFloat64(row["prompt_tokens"])
	completionTokens := toFloat64(row["completion_tokens"])
	isStream := toBool(row["is_stream"])
	other := parseOtherJSON(row["other"])
	frtSeconds := toFloat64(other["frt"]) / 1000.0

	firstTokenTime := useTime
	if isStream && frtSeconds > 0 {
		firstTokenTime = frtSeconds
	}
	if firstTokenTime > 0 {
		stats.timedRequests++
		if firstTokenTime <= 5 {
			stats.within5s++
		}
		if firstTokenTime <= 10 {
			stats.within10s++
		}
	}

	if useTime > 0 {
		stats.durationTimedRequests++
		if useTime <= 10 {
			stats.durationWithin10s++
		}
		if useTime <= 20 {
			stats.durationWithin20s++
		}
	}

	if completionTokens > 0 && useTime > 0 {
		effectiveTime := useTime
		if isStream && frtSeconds > 0 {
			effectiveTime = math.Max(useTime-frtSeconds, 1)
		}
		stats.outputRequests++
		stats.completionTokensSum += completionTokens
		stats.useTimeSum += effectiveTime
	}

	cacheTokens := toFloat64(other["cache_tokens"])
	if cacheTokens > 0 {
		stats.cacheTokensSum += cacheTokens
	}
	if isClaudeMessagesRequest(other) {
		stats.cacheDenominatorSum += promptTokens + cacheTokens
	} else {
		stats.cacheDenominatorSum += math.Max(promptTokens, cacheTokens)
	}
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

	return s.db.Query(query, args...)
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
				accumulatePerformanceRow(stats, row)
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

func (s *ModelStatusService) GetChannelPerformanceSummaries(window string) ([]map[string]interface{}, error) {
	cacheKey := fmt.Sprintf("model_status:channel_performance:%s", window)
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	twConfig, ok := timeWindowConfigs[window]
	if !ok {
		twConfig = timeWindowConfigs["24h"]
	}

	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds

	statsByChannel := make(map[int64]*performanceStats)
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
			channelID := toInt64(row["channel_id"])
			stats := statsByChannel[channelID]
			if stats == nil {
				stats = &performanceStats{}
				statsByChannel[channelID] = stats
			}
			accumulatePerformanceRow(stats, row)
		}
		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
	}

	channelIDs := make([]int64, 0, len(statsByChannel))
	for channelID := range statsByChannel {
		channelIDs = append(channelIDs, channelID)
	}
	channelNames := s.getChannelNameMap(channelIDs)

	type channelStat struct {
		id    int64
		stats *performanceStats
	}
	ordered := make([]channelStat, 0, len(statsByChannel))
	for channelID, stats := range statsByChannel {
		if stats.successRequests > 0 {
			ordered = append(ordered, channelStat{id: channelID, stats: stats})
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].stats.successRequests == ordered[j].stats.successRequests {
			return ordered[i].id < ordered[j].id
		}
		return ordered[i].stats.successRequests > ordered[j].stats.successRequests
	})

	results := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		perf := performanceSummaryFromStats(item.stats)
		channelName := channelNames[item.id]
		if channelName == "" {
			channelName = fmt.Sprintf("Channel#%d", item.id)
		}
		results = append(results, map[string]interface{}{
			"channel_id":               item.id,
			"channel_name":             channelName,
			"total_requests":           perf["total_requests"],
			"within_5s_rate":           perf["within_5s_rate"],
			"within_10s_rate":          perf["within_10s_rate"],
			"duration_within_10s_rate": perf["duration_within_10s_rate"],
			"duration_within_20s_rate": perf["duration_within_20s_rate"],
			"cache_hit_rate":           perf["cache_hit_rate"],
			"completion_tps":           perf["completion_tps"],
			"timed_requests":           perf["timed_requests"],
			"duration_timed_requests":  perf["duration_timed_requests"],
			"output_requests":          perf["output_requests"],
		})
	}

	cm.Set(cacheKey, results, 30*time.Second)
	return results, nil
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
	cacheKey := fmt.Sprintf("model_status:%s:%s", modelName, window)
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	// Get window configuration (dynamic slot count per window)
	twConfig, ok := timeWindowConfigs[window]
	if !ok {
		twConfig = timeWindowConfigs["24h"]
	}

	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	numSlots := twConfig.numSlots
	slotSeconds := twConfig.slotSeconds

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
			SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty
		FROM logs
		WHERE model_name = ?
			AND created_at >= ? AND created_at < ?
			AND type IN (2, 5)
		GROUP BY FLOOR((created_at - %d) / %d)`,
		startTime, slotSeconds,
		startTime, slotSeconds))

	rows, _ := s.db.Query(slotQuery, modelName, startTime, now)

	// Initialize all slots with zeros
	type slotInfo struct {
		total   int64
		success int64
		failure int64
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
	totalEmpty := int64(0)

	for i := 0; i < numSlots; i++ {
		slotStart := startTime + int64(i)*slotSeconds
		slotEnd := slotStart + slotSeconds

		si := slotMap[int64(i)]
		slotTotal := int64(0)
		slotSuccess := int64(0)
		slotFailure := int64(0)
		slotEmpty := int64(0)
		if si != nil {
			slotTotal = si.total
			slotSuccess = si.success
			slotFailure = si.failure
			slotEmpty = si.empty
		}

		slotRate := float64(100)
		if slotTotal > 0 {
			slotRate = float64(slotSuccess) / float64(slotTotal) * 100
		}

		slotData = append(slotData, map[string]interface{}{
			"slot":           i,
			"start_time":     slotStart,
			"end_time":       slotEnd,
			"total_requests": slotTotal,
			"success_count":  slotSuccess,
			"failure_count":  slotFailure,
			"empty_count":    slotEmpty,
			"success_rate":   roundRate(slotRate),
			"status":         getStatusColor(slotRate, slotTotal),
		})

		totalReqs += slotTotal
		totalSuccess += slotSuccess
		totalFailure += slotFailure
		totalEmpty += slotEmpty
	}

	overallRate := float64(100)
	if totalReqs > 0 {
		overallRate = float64(totalSuccess) / float64(totalReqs) * 100
	}

	perf := s.getModelPerformanceMap([]string{modelName}, startTime, now)[modelName]

	result := map[string]interface{}{
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              window,
		"total_requests":           totalReqs,
		"success_count":            totalSuccess,
		"failure_count":            totalFailure,
		"empty_count":              totalEmpty,
		"success_rate":             roundRate(overallRate),
		"current_status":           getStatusColor(overallRate, totalReqs),
		"within_5s_rate":           perf["within_5s_rate"],
		"within_10s_rate":          perf["within_10s_rate"],
		"duration_within_10s_rate": perf["duration_within_10s_rate"],
		"duration_within_20s_rate": perf["duration_within_20s_rate"],
		"cache_hit_rate":           perf["cache_hit_rate"],
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
	twConfig, ok := timeWindowConfigs[window]
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
	}
}

// SetTimeWindow saves time window to cache
func (s *ModelStatusService) SetTimeWindow(window string) {
	cm := cache.Get()
	cm.Set("model_status:time_window", window, 0)
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
