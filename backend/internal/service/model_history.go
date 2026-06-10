package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/config"
	"github.com/new-api-tools/backend/internal/logger"

	_ "modernc.org/sqlite"
)

// ModelHistoryService persists daily model-monitor snapshots into a local
// SQLite database so historical (per-day, hour-level) metrics can be queried
// without scanning the main logs table repeatedly. The main MySQL/PostgreSQL
// database is never written to — only read during aggregation.
type ModelHistoryService struct {
	db *sql.DB
}

const historySlotCount = 24 // hourly slots per day
const historySlotSeconds = 3600

var (
	historyOnce sync.Once
	historyInst *ModelHistoryService
	historyErr  error
)

// dailyPerfStats mirrors the raw accumulators of performanceStats plus the
// availability counters, so we can store raw sums and recompute rates with the
// exact same buildPerformanceSummary used by the live path.
type dailyPerfStats struct {
	totalRequests int64
	successCount  int64
	failureCount  int64
	emptyCount    int64

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
	completionTokensSum   float64
	useTimeSum            float64
}

// GetModelHistoryService returns the lazily-initialized singleton. It opens
// (and creates if missing) the SQLite database and ensures the schema exists.
func GetModelHistoryService() (*ModelHistoryService, error) {
	historyOnce.Do(func() {
		historyInst, historyErr = newModelHistoryService()
	})
	return historyInst, historyErr
}

func newModelHistoryService() (*ModelHistoryService, error) {
	cfg := config.Get()
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir failed: %w", err)
	}

	dbPath := filepath.Join(dataDir, "model_history.db")
	// _pragma options keep concurrent reads/writes safe for the single writer
	// (daily aggregation) + many readers (HTTP queries) access pattern.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite failed: %w", err)
	}
	// SQLite handles concurrency best with a single writer connection.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	svc := &ModelHistoryService{db: db}
	if err := svc.ensureSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensure schema failed: %w", err)
	}
	logger.L.System(fmt.Sprintf("[模型历史] SQLite 历史库已就绪: %s", dbPath))
	return svc, nil
}

func (s *ModelHistoryService) ensureSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS model_daily_summary (
			date TEXT NOT NULL,
			model_name TEXT NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			empty_count INTEGER NOT NULL DEFAULT 0,
			timed_requests INTEGER NOT NULL DEFAULT 0,
			within_5s INTEGER NOT NULL DEFAULT 0,
			within_10s INTEGER NOT NULL DEFAULT 0,
			duration_timed_requests INTEGER NOT NULL DEFAULT 0,
			duration_within_10s INTEGER NOT NULL DEFAULT 0,
			duration_within_20s INTEGER NOT NULL DEFAULT 0,
			output_requests INTEGER NOT NULL DEFAULT 0,
			claude_requests INTEGER NOT NULL DEFAULT 0,
			cache_denominator_sum REAL NOT NULL DEFAULT 0,
			cache_tokens_sum REAL NOT NULL DEFAULT 0,
			cache_write_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			start_time INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model_name)
		)`,
		`CREATE TABLE IF NOT EXISTS model_hourly_slot (
			date TEXT NOT NULL,
			model_name TEXT NOT NULL,
			slot_idx INTEGER NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			empty_count INTEGER NOT NULL DEFAULT 0,
			timed_requests INTEGER NOT NULL DEFAULT 0,
			within_5s INTEGER NOT NULL DEFAULT 0,
			within_10s INTEGER NOT NULL DEFAULT 0,
			duration_timed_requests INTEGER NOT NULL DEFAULT 0,
			duration_within_10s INTEGER NOT NULL DEFAULT 0,
			duration_within_20s INTEGER NOT NULL DEFAULT 0,
			output_requests INTEGER NOT NULL DEFAULT 0,
			claude_requests INTEGER NOT NULL DEFAULT 0,
			cache_denominator_sum REAL NOT NULL DEFAULT 0,
			cache_tokens_sum REAL NOT NULL DEFAULT 0,
			cache_write_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model_name, slot_idx)
		)`,
		`CREATE TABLE IF NOT EXISTS model_daily_channel (
			date TEXT NOT NULL,
			channel_id INTEGER NOT NULL,
			channel_name TEXT NOT NULL DEFAULT '',
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			empty_count INTEGER NOT NULL DEFAULT 0,
			timed_requests INTEGER NOT NULL DEFAULT 0,
			within_5s INTEGER NOT NULL DEFAULT 0,
			within_10s INTEGER NOT NULL DEFAULT 0,
			duration_timed_requests INTEGER NOT NULL DEFAULT 0,
			duration_within_10s INTEGER NOT NULL DEFAULT 0,
			duration_within_20s INTEGER NOT NULL DEFAULT 0,
			output_requests INTEGER NOT NULL DEFAULT 0,
			claude_requests INTEGER NOT NULL DEFAULT 0,
			cache_denominator_sum REAL NOT NULL DEFAULT 0,
			cache_tokens_sum REAL NOT NULL DEFAULT 0,
			cache_write_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, channel_id)
		)`,
		`CREATE TABLE IF NOT EXISTS channel_hourly_slot (
			date TEXT NOT NULL,
			channel_id INTEGER NOT NULL,
			slot_idx INTEGER NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			empty_count INTEGER NOT NULL DEFAULT 0,
			timed_requests INTEGER NOT NULL DEFAULT 0,
			within_5s INTEGER NOT NULL DEFAULT 0,
			within_10s INTEGER NOT NULL DEFAULT 0,
			duration_timed_requests INTEGER NOT NULL DEFAULT 0,
			duration_within_10s INTEGER NOT NULL DEFAULT 0,
			duration_within_20s INTEGER NOT NULL DEFAULT 0,
			output_requests INTEGER NOT NULL DEFAULT 0,
			claude_requests INTEGER NOT NULL DEFAULT 0,
			cache_denominator_sum REAL NOT NULL DEFAULT 0,
			cache_tokens_sum REAL NOT NULL DEFAULT 0,
			cache_write_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, channel_id, slot_idx)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_summary_date ON model_daily_summary(date)`,
		`CREATE INDEX IF NOT EXISTS idx_hourly_slot_date_model ON model_hourly_slot(date, model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_channel_date ON model_daily_channel(date)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_hourly_slot_date_channel ON channel_hourly_slot(date, channel_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("model_daily_channel", "success_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("model_daily_channel", "failure_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("model_daily_channel", "empty_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	perfColumns := []struct {
		name       string
		definition string
	}{
		{"timed_requests", "INTEGER NOT NULL DEFAULT 0"},
		{"within_5s", "INTEGER NOT NULL DEFAULT 0"},
		{"within_10s", "INTEGER NOT NULL DEFAULT 0"},
		{"duration_timed_requests", "INTEGER NOT NULL DEFAULT 0"},
		{"duration_within_10s", "INTEGER NOT NULL DEFAULT 0"},
		{"duration_within_20s", "INTEGER NOT NULL DEFAULT 0"},
		{"output_requests", "INTEGER NOT NULL DEFAULT 0"},
		{"claude_requests", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_denominator_sum", "REAL NOT NULL DEFAULT 0"},
		{"cache_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"cache_write_sum", "REAL NOT NULL DEFAULT 0"},
		{"completion_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"use_time_sum", "REAL NOT NULL DEFAULT 0"},
	}
	for _, table := range []string{"model_hourly_slot", "channel_hourly_slot"} {
		for _, col := range perfColumns {
			if err := s.ensureColumn(table, col.name, col.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ModelHistoryService) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

// Close closes the underlying SQLite database connection.
func (s *ModelHistoryService) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// HasDate reports whether any summary rows exist for the given date.
func (s *ModelHistoryService) HasDate(date string) (bool, error) {
	row := s.db.QueryRow(`SELECT 1 FROM model_daily_summary WHERE date = ? LIMIT 1`, date)
	var x int
	err := row.Scan(&x)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListAvailableDates returns distinct dates that have stored data, newest first.
func (s *ModelHistoryService) ListAvailableDates() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT date FROM model_daily_summary ORDER BY date DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := make([]string, 0)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		dates = append(dates, d)
	}
	return dates, rows.Err()
}

// daySnapshot bundles everything aggregated for a single day before it is
// written transactionally.
type daySnapshot struct {
	date     string
	startTS  int64
	models   map[string]*dailyPerfStats
	slots    map[string]map[int]*slotCounts // model -> slotIdx -> counts
	channels map[int64]*dailyPerfStats
	chanSlot map[int64]map[int]*slotCounts // channel -> slotIdx -> counts
	chanName map[int64]string
}

type slotCounts struct {
	total   int64
	success int64
	failure int64
	empty   int64

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
	completionTokensSum   float64
	useTimeSum            float64
}

// SaveDay persists a day snapshot transactionally, replacing any existing rows
// for that date (idempotent re-aggregation).
func (s *ModelHistoryService) SaveDay(snap *daySnapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"model_daily_summary", "model_hourly_slot", "model_daily_channel", "channel_hourly_slot"} {
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE date = ?", table), snap.date); err != nil {
			return err
		}
	}

	summaryStmt, err := tx.Prepare(`INSERT INTO model_daily_summary (
		date, model_name, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum, start_time
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer summaryStmt.Close()
	for name, st := range snap.models {
		if _, err := summaryStmt.Exec(
			snap.date, name, st.totalRequests, st.successCount, st.failureCount, st.emptyCount,
			st.timedRequests, st.within5s, st.within10s, st.durationTimedRequests,
			st.durationWithin10s, st.durationWithin20s, st.outputRequests, st.claudeRequests,
			st.cacheDenominatorSum, st.cacheTokensSum, st.cacheWriteSum,
			st.completionTokensSum, st.useTimeSum, snap.startTS,
		); err != nil {
			return err
		}
	}

	slotStmt, err := tx.Prepare(`INSERT INTO model_hourly_slot (
		date, model_name, slot_idx, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer slotStmt.Close()
	for name, slots := range snap.slots {
		for idx, c := range slots {
			if _, err := slotStmt.Exec(
				snap.date, name, idx, c.total, c.success, c.failure, c.empty,
				c.timedRequests, c.within5s, c.within10s, c.durationTimedRequests,
				c.durationWithin10s, c.durationWithin20s, c.outputRequests, c.claudeRequests,
				c.cacheDenominatorSum, c.cacheTokensSum, c.cacheWriteSum,
				c.completionTokensSum, c.useTimeSum,
			); err != nil {
				return err
			}
		}
	}

	chanStmt, err := tx.Prepare(`INSERT INTO model_daily_channel (
		date, channel_id, channel_name, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s,
		duration_timed_requests, duration_within_10s, duration_within_20s, output_requests,
		claude_requests, cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanStmt.Close()
	for id, st := range snap.channels {
		if _, err := chanStmt.Exec(
			snap.date, id, snap.chanName[id], st.totalRequests, st.successCount, st.failureCount, st.emptyCount,
			st.timedRequests, st.within5s, st.within10s,
			st.durationTimedRequests, st.durationWithin10s, st.durationWithin20s, st.outputRequests,
			st.claudeRequests, st.cacheDenominatorSum, st.cacheTokensSum, st.cacheWriteSum,
			st.completionTokensSum, st.useTimeSum,
		); err != nil {
			return err
		}
	}

	chanSlotStmt, err := tx.Prepare(`INSERT INTO channel_hourly_slot (
		date, channel_id, slot_idx, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanSlotStmt.Close()
	for channelID, slots := range snap.chanSlot {
		for idx, c := range slots {
			if _, err := chanSlotStmt.Exec(
				snap.date, channelID, idx, c.total, c.success, c.failure, c.empty,
				c.timedRequests, c.within5s, c.within10s, c.durationTimedRequests,
				c.durationWithin10s, c.durationWithin20s, c.outputRequests, c.claudeRequests,
				c.cacheDenominatorSum, c.cacheTokensSum, c.cacheWriteSum,
				c.completionTokensSum, c.useTimeSum,
			); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// perfSummaryFromDaily recomputes the performance summary map from stored raw
// accumulators using the same builder as the live path.
func perfSummaryFromDaily(st *dailyPerfStats) map[string]interface{} {
	if st == nil {
		st = &dailyPerfStats{}
	}
	return buildPerformanceSummary(
		st.successCount, // total_requests in summary == successful (type=2) requests, matches live
		st.timedRequests,
		st.outputRequests,
		st.within5s,
		st.within10s,
		st.durationTimedRequests,
		st.durationWithin10s,
		st.durationWithin20s,
		st.claudeRequests,
		st.cacheDenominatorSum,
		st.cacheTokensSum,
		st.cacheWriteSum,
		st.completionTokensSum,
		st.useTimeSum,
	)
}

// GetAvailableModelsByDate returns models that have stored data for the date,
// ordered by request count desc — mirrors GetAvailableModels output shape.
func (s *ModelHistoryService) GetAvailableModelsByDate(date string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`SELECT model_name, total_requests
		FROM model_daily_summary WHERE date = ? ORDER BY total_requests DESC`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]interface{}, 0)
	for rows.Next() {
		var name string
		var total int64
		if err := rows.Scan(&name, &total); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"model_name":        name,
			"request_count_24h": total,
		})
	}
	return result, rows.Err()
}

// getModelSummaryRow loads the stored summary stats for one model/date.
func (s *ModelHistoryService) getModelSummaryRow(date, modelName string) (*dailyPerfStats, int64, bool, error) {
	row := s.db.QueryRow(`SELECT total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum, start_time
		FROM model_daily_summary WHERE date = ? AND model_name = ?`, date, modelName)
	st := &dailyPerfStats{}
	var startTS int64
	err := row.Scan(&st.totalRequests, &st.successCount, &st.failureCount, &st.emptyCount,
		&st.timedRequests, &st.within5s, &st.within10s, &st.durationTimedRequests,
		&st.durationWithin10s, &st.durationWithin20s, &st.outputRequests, &st.claudeRequests,
		&st.cacheDenominatorSum, &st.cacheTokensSum, &st.cacheWriteSum,
		&st.completionTokensSum, &st.useTimeSum, &startTS)
	if err == sql.ErrNoRows {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return st, startTS, true, nil
}

// getModelSlots loads stored hourly slots for one model/date keyed by slot_idx.
func (s *ModelHistoryService) getModelSlots(date, modelName string) (map[int]*slotCounts, error) {
	rows, err := s.db.Query(`SELECT slot_idx, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
		FROM model_hourly_slot WHERE date = ? AND model_name = ?`, date, modelName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]*slotCounts)
	for rows.Next() {
		var idx int
		c := &slotCounts{}
		if err := rows.Scan(
			&idx, &c.total, &c.success, &c.failure, &c.empty,
			&c.timedRequests, &c.within5s, &c.within10s, &c.durationTimedRequests,
			&c.durationWithin10s, &c.durationWithin20s, &c.outputRequests, &c.claudeRequests,
			&c.cacheDenominatorSum, &c.cacheTokensSum, &c.cacheWriteSum,
			&c.completionTokensSum, &c.useTimeSum,
		); err != nil {
			return nil, err
		}
		out[idx] = c
	}
	return out, rows.Err()
}

// buildModelStatusFromHistory assembles a response identical in shape to the
// live GetModelStatus output, using stored data. If the model has no row for
// the date, a zero-filled status is returned so the grid layout stays stable.
func (s *ModelHistoryService) buildModelStatusFromHistory(date, modelName string, startTS int64) (map[string]interface{}, error) {
	st, storedStart, found, err := s.getModelSummaryRow(date, modelName)
	if err != nil {
		return nil, err
	}
	if found && storedStart > 0 {
		startTS = storedStart
	}
	if !found {
		st = &dailyPerfStats{}
	}

	slotMap, err := s.getModelSlots(date, modelName)
	if err != nil {
		return nil, err
	}

	slotData := buildAvailabilitySlotData(slotMap, startTS, historySlotSeconds, historySlotCount)

	overallRate := float64(100)
	if st.totalRequests > 0 {
		overallRate = float64(st.successCount) / float64(st.totalRequests) * 100
	}
	perf := perfSummaryFromDaily(st)

	return map[string]interface{}{
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              "24h",
		"date":                     date,
		"total_requests":           st.totalRequests,
		"success_count":            st.successCount,
		"failure_count":            st.failureCount,
		"empty_count":              st.emptyCount,
		"success_rate":             roundRate(overallRate),
		"current_status":           getStatusColor(overallRate, st.totalRequests),
		"within_5s_rate":           perf["within_5s_rate"],
		"within_10s_rate":          perf["within_10s_rate"],
		"duration_within_10s_rate": perf["duration_within_10s_rate"],
		"duration_within_20s_rate": perf["duration_within_20s_rate"],
		"cache_hit_rate":           perf["cache_hit_rate"],
		"cache_write_rate":         perf["cache_write_rate"],
		"completion_tps":           perf["completion_tps"],
		"timed_requests":           perf["timed_requests"],
		"duration_timed_requests":  perf["duration_timed_requests"],
		"output_requests":          perf["output_requests"],
		"slot_data":                slotData,
	}, nil
}

// GetMultipleModelsStatusByDate returns historical status grids for the models.
func (s *ModelHistoryService) GetMultipleModelsStatusByDate(modelNames []string, date string) ([]map[string]interface{}, error) {
	startTS := dayStartTimestamp(date)
	results := make([]map[string]interface{}, 0, len(modelNames))
	for _, name := range modelNames {
		status, err := s.buildModelStatusFromHistory(date, name, startTS)
		if err != nil {
			return nil, err
		}
		results = append(results, status)
	}
	return results, nil
}

// GetChannelPerformanceByDate returns stored channel performance for the date,
// ordered by request count desc — mirrors GetChannelPerformanceSummaries.
func (s *ModelHistoryService) GetChannelPerformanceByDate(date string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`SELECT channel_id, channel_name, total_requests, success_count, failure_count, empty_count, timed_requests,
		within_5s, within_10s, duration_timed_requests, duration_within_10s, duration_within_20s,
		output_requests, claude_requests, cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
		FROM model_daily_channel WHERE date = ? ORDER BY total_requests DESC`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type chanRow struct {
		id   int64
		name string
		st   *dailyPerfStats
	}
	ordered := make([]chanRow, 0)
	for rows.Next() {
		var id int64
		var name string
		st := &dailyPerfStats{}
		if err := rows.Scan(&id, &name, &st.totalRequests, &st.successCount, &st.failureCount, &st.emptyCount, &st.timedRequests,
			&st.within5s, &st.within10s, &st.durationTimedRequests, &st.durationWithin10s, &st.durationWithin20s,
			&st.outputRequests, &st.claudeRequests, &st.cacheDenominatorSum, &st.cacheTokensSum, &st.cacheWriteSum,
			&st.completionTokensSum, &st.useTimeSum); err != nil {
			return nil, err
		}
		ordered = append(ordered, chanRow{id: id, name: name, st: st})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		perf := perfSummaryFromDaily(item.st)
		successRate := float64(100)
		if item.st.totalRequests > 0 {
			successRate = float64(item.st.successCount) / float64(item.st.totalRequests) * 100
		}
		name := item.name
		if name == "" {
			name = fmt.Sprintf("Channel#%d", item.id)
		}
		results = append(results, map[string]interface{}{
			"channel_id":               item.id,
			"channel_name":             name,
			"total_requests":           item.st.totalRequests,
			"success_count":            item.st.successCount,
			"failure_count":            item.st.failureCount,
			"empty_count":              item.st.emptyCount,
			"success_rate":             roundRate(successRate),
			"current_status":           getStatusColor(successRate, item.st.totalRequests),
			"within_5s_rate":           perf["within_5s_rate"],
			"within_10s_rate":          perf["within_10s_rate"],
			"duration_within_10s_rate": perf["duration_within_10s_rate"],
			"duration_within_20s_rate": perf["duration_within_20s_rate"],
			"cache_hit_rate":           perf["cache_hit_rate"],
			"cache_write_rate":         perf["cache_write_rate"],
			"completion_tps":           perf["completion_tps"],
			"timed_requests":           perf["timed_requests"],
			"duration_timed_requests":  perf["duration_timed_requests"],
			"output_requests":          perf["output_requests"],
			"slot_data":                buildAvailabilitySlotData(s.getChannelSlots(date, item.id), dayStartTimestamp(date), historySlotSeconds, historySlotCount),
		})
	}
	return results, nil
}

func (s *ModelHistoryService) getChannelSlots(date string, channelID int64) map[int]*slotCounts {
	rows, err := s.db.Query(`SELECT slot_idx, total_requests, success_count, failure_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		completion_tokens_sum, use_time_sum
		FROM channel_hourly_slot WHERE date = ? AND channel_id = ?`, date, channelID)
	if err != nil {
		return map[int]*slotCounts{}
	}
	defer rows.Close()

	out := make(map[int]*slotCounts)
	for rows.Next() {
		var idx int
		c := &slotCounts{}
		if err := rows.Scan(
			&idx, &c.total, &c.success, &c.failure, &c.empty,
			&c.timedRequests, &c.within5s, &c.within10s, &c.durationTimedRequests,
			&c.durationWithin10s, &c.durationWithin20s, &c.outputRequests, &c.claudeRequests,
			&c.cacheDenominatorSum, &c.cacheTokensSum, &c.cacheWriteSum,
			&c.completionTokensSum, &c.useTimeSum,
		); err != nil {
			return map[int]*slotCounts{}
		}
		out[idx] = c
	}
	if err := rows.Err(); err != nil {
		return map[int]*slotCounts{}
	}
	return out
}

// dayStartTimestamp returns the unix timestamp of local midnight for a
// YYYY-MM-DD date string. Falls back to 0 on parse error.
func dayStartTimestamp(date string) int64 {
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}
