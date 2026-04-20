package service

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

// Constants for model status
var (
	AvailableTimeWindows = []string{"15m", "30m", "1h", "6h", "12h", "24h"}
	DefaultTimeWindow    = "24h"
	AvailableThemes = []string{
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

func buildPerformanceSummary(totalRequests, timedRequests, outputRequests, within5s, within10s, completionTokensSum, useTimeSum int64) map[string]interface{} {
	var within5sRate interface{}
	var within10sRate interface{}
	var completionTPS interface{}

	if timedRequests > 0 {
		within5sRate = roundRate(float64(within5s) / float64(timedRequests) * 100)
		within10sRate = roundRate(float64(within10s) / float64(timedRequests) * 100)
	}
	if useTimeSum > 0 {
		completionTPS = roundRate(float64(completionTokensSum) / float64(useTimeSum))
	}

	return map[string]interface{}{
		"total_requests":   totalRequests,
		"within_5s_rate":   within5sRate,
		"within_10s_rate":  within10sRate,
		"completion_tps":   completionTPS,
		"timed_requests":   timedRequests,
		"output_requests":  outputRequests,
	}
}

// ModelStatusService handles model availability monitoring
type ModelStatusService struct {
	db *database.Manager
}

// NewModelStatusService creates a new ModelStatusService
func NewModelStatusService() *ModelStatusService {
	return &ModelStatusService{db: database.Get()}
}

func (s *ModelStatusService) getModelPerformanceMap(modelNames []string, startTime, now int64) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	if len(modelNames) == 0 {
		return result
	}

	placeholders := make([]string, 0, len(modelNames))
	args := make([]interface{}, 0, len(modelNames)+2)
	for i, modelName := range modelNames {
		placeholders = append(placeholders, s.db.Placeholder(i+1))
		args = append(args, modelName)
	}
	args = append(args, startTime, now)

	query := fmt.Sprintf(`
		SELECT
			model_name,
			SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN type = 2 AND use_time > 0 THEN 1 ELSE 0 END) as timed_requests,
			SUM(CASE WHEN type = 2 AND use_time > 0 AND use_time <= 5 THEN 1 ELSE 0 END) as within_5s,
			SUM(CASE WHEN type = 2 AND use_time > 0 AND use_time <= 10 THEN 1 ELSE 0 END) as within_10s,
			SUM(CASE WHEN type = 2 AND completion_tokens > 0 AND use_time > 0 THEN 1 ELSE 0 END) as output_requests,
			COALESCE(SUM(CASE WHEN type = 2 AND completion_tokens > 0 AND use_time > 0 THEN completion_tokens ELSE 0 END), 0) as completion_tokens_sum,
			COALESCE(SUM(CASE WHEN type = 2 AND completion_tokens > 0 AND use_time > 0 THEN use_time ELSE 0 END), 0) as use_time_sum
		FROM logs
		WHERE model_name IN (%s)
			AND created_at >= %s AND created_at < %s
			AND type IN (2, 5)
		GROUP BY model_name`,
		strings.Join(placeholders, ", "),
		s.db.Placeholder(len(modelNames)+1),
		s.db.Placeholder(len(modelNames)+2),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result
	}

	for _, row := range rows {
		modelName := toString(row["model_name"])
		if modelName == "" {
			continue
		}
		result[modelName] = buildPerformanceSummary(
			toInt64(row["success_requests"]),
			toInt64(row["timed_requests"]),
			toInt64(row["output_requests"]),
			toInt64(row["within_5s"]),
			toInt64(row["within_10s"]),
			toInt64(row["completion_tokens_sum"]),
			toInt64(row["use_time_sum"]),
		)
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

	query := s.db.RebindQuery(`
		SELECT
			c.id as channel_id,
			COALESCE(c.name, '') as channel_name,
			SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN l.type = 2 AND l.use_time > 0 THEN 1 ELSE 0 END) as timed_requests,
			SUM(CASE WHEN l.type = 2 AND l.use_time > 0 AND l.use_time <= 5 THEN 1 ELSE 0 END) as within_5s,
			SUM(CASE WHEN l.type = 2 AND l.use_time > 0 AND l.use_time <= 10 THEN 1 ELSE 0 END) as within_10s,
			SUM(CASE WHEN l.type = 2 AND l.completion_tokens > 0 AND l.use_time > 0 THEN 1 ELSE 0 END) as output_requests,
			COALESCE(SUM(CASE WHEN l.type = 2 AND l.completion_tokens > 0 AND l.use_time > 0 THEN l.completion_tokens ELSE 0 END), 0) as completion_tokens_sum,
			COALESCE(SUM(CASE WHEN l.type = 2 AND l.completion_tokens > 0 AND l.use_time > 0 THEN l.use_time ELSE 0 END), 0) as use_time_sum
		FROM logs l
		INNER JOIN channels c ON c.id = l.channel_id
		WHERE l.created_at >= ? AND l.created_at < ?
			AND l.type IN (2, 5)
		GROUP BY c.id, c.name
		HAVING SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) > 0
		ORDER BY success_requests DESC, channel_id ASC`)

	rows, err := s.db.Query(query, startTime, now)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		perf := buildPerformanceSummary(
			toInt64(row["success_requests"]),
			toInt64(row["timed_requests"]),
			toInt64(row["output_requests"]),
			toInt64(row["within_5s"]),
			toInt64(row["within_10s"]),
			toInt64(row["completion_tokens_sum"]),
			toInt64(row["use_time_sum"]),
		)
		channelName := toString(row["channel_name"])
		if channelName == "" {
			channelName = fmt.Sprintf("Channel#%d", toInt64(row["channel_id"]))
		}
		results = append(results, map[string]interface{}{
			"channel_id":      toInt64(row["channel_id"]),
			"channel_name":    channelName,
			"total_requests":  perf["total_requests"],
			"within_5s_rate":  perf["within_5s_rate"],
			"within_10s_rate": perf["within_10s_rate"],
			"completion_tps":  perf["completion_tps"],
			"timed_requests":  perf["timed_requests"],
			"output_requests": perf["output_requests"],
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
		"model_name":     modelName,
		"display_name":   modelName,
		"time_window":    window,
		"total_requests": totalReqs,
		"success_count":  totalSuccess,
		"failure_count":  totalFailure,
		"empty_count":    totalEmpty,
		"success_rate":   roundRate(overallRate),
		"current_status": getStatusColor(overallRate, totalReqs),
		"within_5s_rate": perf["within_5s_rate"],
		"within_10s_rate": perf["within_10s_rate"],
		"completion_tps": perf["completion_tps"],
		"timed_requests": perf["timed_requests"],
		"output_requests": perf["output_requests"],
		"slot_data":      slotData,
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
			status["completion_tps"] = perf["completion_tps"]
			status["timed_requests"] = perf["timed_requests"]
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
