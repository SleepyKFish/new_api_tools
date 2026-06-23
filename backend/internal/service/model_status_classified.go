package service

import (
	"fmt"
	"strings"
	"time"
)

// 本文件在不改动现有 GetModelStatus / 老监控逻辑的前提下，额外提供「带错误分类」
// 的模型状态。它复用现有的 slot 聚合结果，只对 type=5 失败行做一次轻量扫描
// (失败通常只占总量很小一部分)，按热更新规则归类为:
//
//	model_error_count   模型侧错误 (含按口径计入的限速)
//	format_error_count  用户请求格式错误 (不计入成功率分母)
//	rate_limit_count    限速
//
// 并据此给出 model_success_rate = success / (total - format_error[- rate_limit])。

type errorClassCounts struct {
	modelError int64
	userFormat int64
	rateLimit  int64
}

// modelSuccessRate 按口径计算「模型侧成功率」。
//
//	分母 = total - 用户格式错误 (- 限速，当限速不计入模型错误时)
//	分子 = success (严格定义: type=2 且有输出)
//
// 用户格式错误是用户自身问题，不该拉低模型可用性，故始终从分母剔除。
func modelSuccessRate(success, total, userFormat, rateLimit int64, countRateLimitAsModel bool) float64 {
	denom := total - userFormat
	if !countRateLimitAsModel {
		denom -= rateLimit
	}
	if denom <= 0 {
		return 100
	}
	rate := float64(success) / float64(denom) * 100
	if rate < 0 {
		return 0
	}
	if rate > 100 {
		return 100
	}
	return rate
}

// GetClassifiedModelsStatus 返回带错误分类字段的多模型状态。
// 在 GetMultipleModelsStatus 结果之上叠加分类计数与 model_success_rate (含 slot 级)。
func (s *ModelStatusService) GetClassifiedModelsStatus(modelNames []string, window string) ([]map[string]interface{}, error) {
	base, err := s.GetMultipleModelsStatus(modelNames, window)
	if err != nil {
		return nil, err
	}

	rules := GetErrorRules()
	twConfig, ok := ParseTimeWindow(window)
	if !ok {
		twConfig = timeWindowConfigs["24h"]
	}
	now := time.Now().Unix()
	startTime := now - twConfig.totalSeconds
	slotSeconds := twConfig.slotSeconds
	numSlots := twConfig.numSlots

	overall, perSlot := s.classifyFailures(modelNames, startTime, now, slotSeconds, numSlots, rules)

	for _, status := range base {
		modelName := toString(status["model_name"])
		oc := overall[modelName]
		if oc == nil {
			oc = &errorClassCounts{}
		}
		total := toInt64(status["total_requests"])
		success := toInt64(status["success_count"])

		status["model_error_count"] = oc.modelError
		status["format_error_count"] = oc.userFormat
		status["rate_limit_count"] = oc.rateLimit
		status["model_success_rate"] = roundRate(
			modelSuccessRate(success, total, oc.userFormat, oc.rateLimit, rules.CountRateLimitAsModelError))

		// 给每个 slot 叠加分类字段 + 模型侧成功率/颜色。
		// 注意: GetModelStatus 命中缓存时 slot_data 会是 []interface{} (JSON 反序列化),
		// 未命中时是 []map[string]interface{}，两种都要处理。
		for _, slot := range normalizeSlotData(status["slot_data"]) {
			idx := toInt(slot["slot"])
			var sc *errorClassCounts
			if slotClass := perSlot[modelName]; slotClass != nil {
				sc = slotClass[idx]
			}
			if sc == nil {
				sc = &errorClassCounts{}
			}
			slotTotal := toInt64(slot["total_requests"])
			slotSuccess := toInt64(slot["success_count"])
			slot["model_error_count"] = sc.modelError
			slot["format_error_count"] = sc.userFormat
			slot["rate_limit_count"] = sc.rateLimit
			msr := modelSuccessRate(slotSuccess, slotTotal, sc.userFormat, sc.rateLimit, rules.CountRateLimitAsModelError)
			slot["model_success_rate"] = roundRate(msr)
			slot["model_status"] = getStatusColor(msr, slotTotal-sc.userFormat)
		}

		// 顶层模型颜色也改用模型侧成功率 (排除用户格式错误后的口径)
		msrTop := modelSuccessRate(success, total, oc.userFormat, oc.rateLimit, rules.CountRateLimitAsModelError)
		status["model_current_status"] = getStatusColor(msrTop, total-oc.userFormat)
	}

	return base, nil
}

// classifyFailures 扫描窗口内 type=5 失败行，按规则归类，返回每模型的总计数与 slot 级计数。
func (s *ModelStatusService) classifyFailures(modelNames []string, startTime, now, slotSeconds int64, numSlots int, rules ErrorRuleConfig) (map[string]*errorClassCounts, map[string]map[int]*errorClassCounts) {
	overall := make(map[string]*errorClassCounts, len(modelNames))
	perSlot := make(map[string]map[int]*errorClassCounts, len(modelNames))
	for _, name := range modelNames {
		overall[name] = &errorClassCounts{}
		perSlot[name] = make(map[int]*errorClassCounts)
	}

	cursorCreatedAt := startTime - 1
	cursorID := int64(0)
	for {
		rows, err := s.fetchFailureLogBatch(startTime, now, cursorCreatedAt, cursorID, modelNames)
		if err != nil || len(rows) == 0 {
			break
		}
		for _, row := range rows {
			modelName := toString(row["model_name"])
			oc, ok := overall[modelName]
			if !ok {
				continue
			}
			other := parseOtherJSON(row["other"])
			bucket := rules.classifyOtherJSON(other)

			createdAt := toInt64(row["created_at"])
			slotIdx := int((createdAt - startTime) / slotSeconds)
			if slotIdx < 0 {
				slotIdx = 0
			}
			if slotIdx >= numSlots {
				slotIdx = numSlots - 1
			}
			sc := perSlot[modelName][slotIdx]
			if sc == nil {
				sc = &errorClassCounts{}
				perSlot[modelName][slotIdx] = sc
			}

			switch bucket {
			case ErrorBucketRateLimit:
				oc.rateLimit++
				sc.rateLimit++
			case ErrorBucketUserFormat:
				oc.userFormat++
				sc.userFormat++
			default:
				oc.modelError++
				sc.modelError++
			}
		}
		lastRow := rows[len(rows)-1]
		cursorCreatedAt = toInt64(lastRow["created_at"])
		cursorID = toInt64(lastRow["id"])
		if len(rows) < performanceLogBatchSize {
			break
		}
	}

	return overall, perSlot
}

// fetchFailureLogBatch 仅拉取 type=5 失败行 (含 other)，游标分页，与性能扫描同构。
func (s *ModelStatusService) fetchFailureLogBatch(startTime, now, cursorCreatedAt, cursorID int64, modelNames []string) ([]map[string]interface{}, error) {
	otherCol := "NULL as other"
	if s.db.ColumnExists("logs", "other") {
		otherCol = "other"
	}
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
		SELECT id, created_at, model_name, %s
		FROM logs
		WHERE created_at >= ? AND created_at < ?
			AND type = 5
			AND (
				created_at > ?
				OR (created_at = ? AND id > ?)
			)
			%s
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, otherCol, modelFilter))

	return s.db.Query(query, args...)
}

// toInt 是 toInt64 的 int 版本，便于 slot 索引处理。
func toInt(v interface{}) int {
	return int(toInt64(v))
}

// normalizeSlotData 把 slot_data 统一成 []map[string]interface{}，兼容:
//   - 新建结果: []map[string]interface{}
//   - 缓存结果(JSON 反序列化): []interface{} (元素为 map[string]interface{})
//
// 返回的是对底层 map 的引用，原地修改即对调用方可见。
func normalizeSlotData(v interface{}) []map[string]interface{} {
	switch slots := v.(type) {
	case []map[string]interface{}:
		return slots
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(slots))
		for _, item := range slots {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}
