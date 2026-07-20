package service

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/config"
	"github.com/new-api-tools/backend/internal/logger"

	_ "modernc.org/sqlite"
)

var ErrHistoryChannelModelDetailNotBuilt = errors.New("history channel model detail not built")

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
	formatError   int64
	rateLimit     int64
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
	cacheWriteTokensSum   float64
	inputTokensSum        float64
	outputTokensSum       float64
	completionTokensSum   float64
	useTimeSum            float64
	quotaSum              float64
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
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			start_time INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model_name)
		)`,
		`CREATE TABLE IF NOT EXISTS model_daily_totals (
			date TEXT PRIMARY KEY,
			unique_users INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS model_hourly_slot (
			date TEXT NOT NULL,
			model_name TEXT NOT NULL,
			slot_idx INTEGER NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
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
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
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
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, channel_id, slot_idx)
		)`,
		`CREATE TABLE IF NOT EXISTS model_daily_channel_model (
			date TEXT NOT NULL,
			channel_id INTEGER NOT NULL,
			model_name TEXT NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, channel_id, model_name)
		)`,
		`CREATE TABLE IF NOT EXISTS channel_model_hourly_slot (
			date TEXT NOT NULL,
			channel_id INTEGER NOT NULL,
			model_name TEXT NOT NULL,
			slot_idx INTEGER NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0,
			format_error_count INTEGER NOT NULL DEFAULT 0,
			rate_limit_count INTEGER NOT NULL DEFAULT 0,
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
			cache_write_tokens_sum REAL NOT NULL DEFAULT 0,
			input_tokens_sum REAL NOT NULL DEFAULT 0,
			output_tokens_sum REAL NOT NULL DEFAULT 0,
			completion_tokens_sum REAL NOT NULL DEFAULT 0,
			quota_sum REAL NOT NULL DEFAULT 0,
			use_time_sum REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (date, channel_id, model_name, slot_idx)
		)`,
		`CREATE TABLE IF NOT EXISTS model_history_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_summary_date ON model_daily_summary(date)`,
		`CREATE INDEX IF NOT EXISTS idx_hourly_slot_date_model ON model_hourly_slot(date, model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_channel_date ON model_daily_channel(date)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_hourly_slot_date_channel ON channel_hourly_slot(date, channel_id)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_channel_model_date_channel ON model_daily_channel_model(date, channel_id, total_requests DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_model_hourly_date_channel_model ON channel_model_hourly_slot(date, channel_id, model_name)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	perfColumns := []struct {
		name       string
		definition string
	}{
		{"success_count", "INTEGER NOT NULL DEFAULT 0"},
		{"failure_count", "INTEGER NOT NULL DEFAULT 0"},
		{"format_error_count", "INTEGER NOT NULL DEFAULT 0"},
		{"rate_limit_count", "INTEGER NOT NULL DEFAULT 0"},
		{"empty_count", "INTEGER NOT NULL DEFAULT 0"},
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
		{"cache_write_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"input_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"output_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"completion_tokens_sum", "REAL NOT NULL DEFAULT 0"},
		{"quota_sum", "REAL NOT NULL DEFAULT 0"},
		{"use_time_sum", "REAL NOT NULL DEFAULT 0"},
	}
	for _, table := range []string{"model_daily_summary", "model_hourly_slot", "model_daily_channel", "channel_hourly_slot", "model_daily_channel_model", "channel_model_hourly_slot"} {
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
	row := s.db.QueryRow(`SELECT 1 WHERE
		EXISTS (SELECT 1 FROM model_daily_totals WHERE date = ?)
		OR EXISTS (SELECT 1 FROM model_daily_summary WHERE date = ?)`, date, date)
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

// HasDailyTotals reports whether the date was built by the current snapshot
// schema, including dates with no matching model rows.
func (s *ModelHistoryService) HasDailyTotals(date string) (bool, error) {
	row := s.db.QueryRow(`SELECT 1 FROM model_daily_totals WHERE date = ? LIMIT 1`, date)
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

const (
	fullHistoryBackfillMetadataKey   = "full_history_backfill_v2"
	fullHistoryBackfillDateKeyPrefix = fullHistoryBackfillMetadataKey + ":"
)

func (s *ModelHistoryService) hasMetadata(key string) (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM model_history_metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *ModelHistoryService) markMetadata(key string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO model_history_metadata (key, value) VALUES (?, ?)`,
		key, time.Now().UTC().Format(time.RFC3339))
	return err
}

// IsFullHistoryBackfillComplete reports whether every completed source-log
// date has been rebuilt at least once by the v2 full-history task.
func (s *ModelHistoryService) IsFullHistoryBackfillComplete() (bool, error) {
	return s.hasMetadata(fullHistoryBackfillMetadataKey)
}

// IsFullHistoryBackfillDateComplete supports day-level resume after shutdown.
func (s *ModelHistoryService) IsFullHistoryBackfillDateComplete(date string) (bool, error) {
	return s.hasMetadata(fullHistoryBackfillDateKeyPrefix + date)
}

func (s *ModelHistoryService) MarkFullHistoryBackfillComplete() error {
	return s.markMetadata(fullHistoryBackfillMetadataKey)
}

func (s *ModelHistoryService) MarkFullHistoryBackfillDateComplete(date string) error {
	return s.markMetadata(fullHistoryBackfillDateKeyPrefix + date)
}

// daySnapshot bundles everything aggregated for a single day before it is
// written transactionally.
type daySnapshot struct {
	date          string
	startTS       int64
	uniqueUsers   map[int64]struct{}
	models        map[string]*dailyPerfStats
	slots         map[string]map[int]*slotCounts // model -> slotIdx -> counts
	channels      map[int64]*dailyPerfStats
	chanSlot      map[int64]map[int]*slotCounts // channel -> slotIdx -> counts
	chanName      map[int64]string
	channelModels map[int64]map[string]*dailyPerfStats
	chanModelSlot map[int64]map[string]map[int]*slotCounts
}

type slotCounts struct {
	total       int64
	success     int64
	failure     int64
	formatError int64
	rateLimit   int64
	empty       int64

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
	quotaSum              float64
}

// SaveDay persists a day snapshot transactionally, replacing any existing rows
// for that date (idempotent re-aggregation).
func (s *ModelHistoryService) SaveDay(snap *daySnapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"model_daily_summary", "model_daily_totals", "model_hourly_slot", "model_daily_channel", "channel_hourly_slot", "model_daily_channel_model", "channel_model_hourly_slot"} {
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE date = ?", table), snap.date); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO model_daily_totals (date, unique_users, completed_at) VALUES (?, ?, ?)`,
		snap.date, len(snap.uniqueUsers), time.Now().Unix()); err != nil {
		return err
	}

	summaryStmt, err := tx.Prepare(`INSERT INTO model_daily_summary (
		date, model_name, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum, start_time
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer summaryStmt.Close()
	for name, st := range snap.models {
		if _, err := summaryStmt.Exec(
			snap.date, name, st.totalRequests, st.successCount, st.failureCount, st.formatError, st.rateLimit, st.emptyCount,
			st.timedRequests, st.within5s, st.within10s, st.durationTimedRequests,
			st.durationWithin10s, st.durationWithin20s, st.outputRequests, st.claudeRequests,
			st.cacheDenominatorSum, st.cacheTokensSum, st.cacheWriteSum,
			st.cacheWriteTokensSum, st.inputTokensSum, st.outputTokensSum,
			st.completionTokensSum, st.quotaSum, st.useTimeSum, snap.startTS,
		); err != nil {
			return err
		}
	}

	slotStmt, err := tx.Prepare(`INSERT INTO model_hourly_slot (
		date, model_name, slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer slotStmt.Close()
	for name, slots := range snap.slots {
		for idx, c := range slots {
			if _, err := slotStmt.Exec(
				snap.date, name, idx, c.total, c.success, c.failure, c.formatError, c.rateLimit, c.empty,
				c.timedRequests, c.within5s, c.within10s, c.durationTimedRequests,
				c.durationWithin10s, c.durationWithin20s, c.outputRequests, c.claudeRequests,
				c.cacheDenominatorSum, c.cacheTokensSum, c.cacheWriteSum,
				c.cacheWriteTokensSum, c.inputTokensSum, c.outputTokensSum,
				c.completionTokensSum, c.quotaSum, c.useTimeSum,
			); err != nil {
				return err
			}
		}
	}

	chanStmt, err := tx.Prepare(`INSERT INTO model_daily_channel (
		date, channel_id, channel_name, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s,
		duration_timed_requests, duration_within_10s, duration_within_20s, output_requests,
		claude_requests, cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanStmt.Close()
	for id, st := range snap.channels {
		if _, err := chanStmt.Exec(
			snap.date, id, snap.chanName[id], st.totalRequests, st.successCount, st.failureCount, st.formatError, st.rateLimit, st.emptyCount,
			st.timedRequests, st.within5s, st.within10s,
			st.durationTimedRequests, st.durationWithin10s, st.durationWithin20s, st.outputRequests,
			st.claudeRequests, st.cacheDenominatorSum, st.cacheTokensSum, st.cacheWriteSum,
			st.cacheWriteTokensSum, st.inputTokensSum, st.outputTokensSum,
			st.completionTokensSum, st.quotaSum, st.useTimeSum,
		); err != nil {
			return err
		}
	}

	chanSlotStmt, err := tx.Prepare(`INSERT INTO channel_hourly_slot (
		date, channel_id, slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanSlotStmt.Close()
	for channelID, slots := range snap.chanSlot {
		for idx, c := range slots {
			if _, err := chanSlotStmt.Exec(
				snap.date, channelID, idx, c.total, c.success, c.failure, c.formatError, c.rateLimit, c.empty,
				c.timedRequests, c.within5s, c.within10s, c.durationTimedRequests,
				c.durationWithin10s, c.durationWithin20s, c.outputRequests, c.claudeRequests,
				c.cacheDenominatorSum, c.cacheTokensSum, c.cacheWriteSum,
				c.cacheWriteTokensSum, c.inputTokensSum, c.outputTokensSum,
				c.completionTokensSum, c.quotaSum, c.useTimeSum,
			); err != nil {
				return err
			}
		}
	}

	chanModelStmt, err := tx.Prepare(`INSERT INTO model_daily_channel_model (
		date, channel_id, model_name, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s,
		duration_timed_requests, duration_within_10s, duration_within_20s, output_requests,
		claude_requests, cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanModelStmt.Close()
	for channelID, models := range snap.channelModels {
		for modelName, st := range models {
			if _, err := chanModelStmt.Exec(
				snap.date, channelID, modelName, st.totalRequests, st.successCount, st.failureCount, st.formatError, st.rateLimit, st.emptyCount,
				st.timedRequests, st.within5s, st.within10s,
				st.durationTimedRequests, st.durationWithin10s, st.durationWithin20s, st.outputRequests,
				st.claudeRequests, st.cacheDenominatorSum, st.cacheTokensSum, st.cacheWriteSum,
				st.cacheWriteTokensSum, st.inputTokensSum, st.outputTokensSum,
				st.completionTokensSum, st.quotaSum, st.useTimeSum,
			); err != nil {
				return err
			}
		}
	}

	chanModelSlotStmt, err := tx.Prepare(`INSERT INTO channel_model_hourly_slot (
		date, channel_id, model_name, slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer chanModelSlotStmt.Close()
	for channelID, models := range snap.chanModelSlot {
		for modelName, slots := range models {
			for idx, c := range slots {
				if _, err := chanModelSlotStmt.Exec(
					snap.date, channelID, modelName, idx, c.total, c.success, c.failure, c.formatError, c.rateLimit, c.empty,
					c.timedRequests, c.within5s, c.within10s, c.durationTimedRequests,
					c.durationWithin10s, c.durationWithin20s, c.outputRequests, c.claudeRequests,
					c.cacheDenominatorSum, c.cacheTokensSum, c.cacheWriteSum,
					c.cacheWriteTokensSum, c.inputTokensSum, c.outputTokensSum,
					c.completionTokensSum, c.quotaSum, c.useTimeSum,
				); err != nil {
					return err
				}
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
		st.cacheWriteTokensSum,
		st.inputTokensSum,
		st.outputTokensSum,
		st.completionTokensSum,
		st.useTimeSum,
		st.quotaSum,
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
	row := s.db.QueryRow(`SELECT total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum, start_time
		FROM model_daily_summary WHERE date = ? AND model_name = ?`, date, modelName)
	st := &dailyPerfStats{}
	var startTS int64
	err := row.Scan(&st.totalRequests, &st.successCount, &st.failureCount, &st.formatError, &st.rateLimit, &st.emptyCount,
		&st.timedRequests, &st.within5s, &st.within10s, &st.durationTimedRequests,
		&st.durationWithin10s, &st.durationWithin20s, &st.outputRequests, &st.claudeRequests,
		&st.cacheDenominatorSum, &st.cacheTokensSum, &st.cacheWriteSum,
		&st.cacheWriteTokensSum, &st.inputTokensSum, &st.outputTokensSum,
		&st.completionTokensSum, &st.quotaSum, &st.useTimeSum, &startTS)
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
	rows, err := s.db.Query(`SELECT slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
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
			&idx, &c.total, &c.success, &c.failure, &c.formatError, &c.rateLimit, &c.empty,
			&c.timedRequests, &c.within5s, &c.within10s, &c.durationTimedRequests,
			&c.durationWithin10s, &c.durationWithin20s, &c.outputRequests, &c.claudeRequests,
			&c.cacheDenominatorSum, &c.cacheTokensSum, &c.cacheWriteSum,
			&c.cacheWriteTokensSum, &c.inputTokensSum, &c.outputTokensSum,
			&c.completionTokensSum, &c.quotaSum, &c.useTimeSum,
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
	rules := GetErrorRules()
	modelRate := modelSuccessRate(st.successCount, st.totalRequests, st.formatError, st.rateLimit, rules)
	modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(st.totalRequests, st.formatError, st.rateLimit, rules))
	perf := perfSummaryFromDaily(st)

	return map[string]interface{}{
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              "24h",
		"date":                     date,
		"total_requests":           st.totalRequests,
		"success_count":            st.successCount,
		"failure_count":            st.failureCount,
		"format_error_count":       st.formatError,
		"rate_limit_count":         st.rateLimit,
		"non_format_failure_count": nonFormatFailureCount(st.failureCount, st.formatError),
		"model_failure_count":      nonFormatFailureCount(st.failureCount, st.formatError),
		"model_error_count":        nonFormatFailureCount(st.failureCount, st.formatError) + st.emptyCount,
		"non_empty_count":          st.successCount,
		"empty_count":              st.emptyCount,
		"success_rate":             roundRate(overallRate),
		"model_success_rate":       roundRate(modelRate),
		"model_availability_rate":  roundRate(modelRate),
		"current_status":           getStatusColor(overallRate, st.totalRequests),
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
		"total_quota":              perf["total_quota"],
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
	rows, err := s.db.Query(`SELECT channel_id, channel_name, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count, timed_requests,
		within_5s, within_10s, duration_timed_requests, duration_within_10s, duration_within_20s,
		output_requests, claude_requests, cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
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
		if err := rows.Scan(&id, &name, &st.totalRequests, &st.successCount, &st.failureCount, &st.formatError, &st.rateLimit, &st.emptyCount, &st.timedRequests,
			&st.within5s, &st.within10s, &st.durationTimedRequests, &st.durationWithin10s, &st.durationWithin20s,
			&st.outputRequests, &st.claudeRequests, &st.cacheDenominatorSum, &st.cacheTokensSum, &st.cacheWriteSum,
			&st.cacheWriteTokensSum, &st.inputTokensSum, &st.outputTokensSum,
			&st.completionTokensSum, &st.quotaSum, &st.useTimeSum); err != nil {
			return nil, err
		}
		ordered = append(ordered, chanRow{id: id, name: name, st: st})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	modelCounts := s.getChannelModelCounts(date)
	results := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		perf := perfSummaryFromDaily(item.st)
		successRate := float64(100)
		if item.st.totalRequests > 0 {
			successRate = float64(item.st.successCount) / float64(item.st.totalRequests) * 100
		}
		rules := GetErrorRules()
		modelRate := modelSuccessRate(item.st.successCount, item.st.totalRequests, item.st.formatError, item.st.rateLimit, rules)
		modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(item.st.totalRequests, item.st.formatError, item.st.rateLimit, rules))
		name := item.name
		if name == "" {
			name = fmt.Sprintf("Channel#%d", item.id)
		}
		results = append(results, map[string]interface{}{
			"channel_id":               item.id,
			"channel_name":             name,
			"model_count":              modelCounts[item.id],
			"total_requests":           item.st.totalRequests,
			"success_count":            item.st.successCount,
			"failure_count":            item.st.failureCount,
			"format_error_count":       item.st.formatError,
			"rate_limit_count":         item.st.rateLimit,
			"non_format_failure_count": nonFormatFailureCount(item.st.failureCount, item.st.formatError),
			"model_failure_count":      nonFormatFailureCount(item.st.failureCount, item.st.formatError),
			"model_error_count":        nonFormatFailureCount(item.st.failureCount, item.st.formatError) + item.st.emptyCount,
			"non_empty_count":          item.st.successCount,
			"empty_count":              item.st.emptyCount,
			"success_rate":             roundRate(successRate),
			"model_success_rate":       roundRate(modelRate),
			"model_availability_rate":  roundRate(modelRate),
			"current_status":           getStatusColor(successRate, item.st.totalRequests),
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
			"total_quota":              perf["total_quota"],
			"slot_data":                buildAvailabilitySlotData(s.getChannelSlots(date, item.id), dayStartTimestamp(date), historySlotSeconds, historySlotCount),
		})
	}
	return results, nil
}

func (s *ModelHistoryService) getChannelModelCounts(date string) map[int64]int {
	rows, err := s.db.Query(`SELECT channel_id, COUNT(*)
		FROM model_daily_channel_model WHERE date = ? GROUP BY channel_id`, date)
	if err != nil {
		return map[int64]int{}
	}
	defer rows.Close()

	out := make(map[int64]int)
	for rows.Next() {
		var channelID int64
		var count int
		if err := rows.Scan(&channelID, &count); err != nil {
			return map[int64]int{}
		}
		out[channelID] = count
	}
	if err := rows.Err(); err != nil {
		return map[int64]int{}
	}
	return out
}

func (s *ModelHistoryService) getHistoryChannelName(date string, channelID int64) (string, bool, error) {
	row := s.db.QueryRow(`SELECT channel_name FROM model_daily_channel WHERE date = ? AND channel_id = ?`, date, channelID)
	var name string
	if err := row.Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if name == "" {
		name = fmt.Sprintf("Channel#%d", channelID)
	}
	return name, true, nil
}

func (s *ModelHistoryService) getChannelModelSlots(date string, channelID int64, modelName string) map[int]*slotCounts {
	rows, err := s.db.Query(`SELECT slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
		FROM channel_model_hourly_slot WHERE date = ? AND channel_id = ? AND model_name = ?`, date, channelID, modelName)
	if err != nil {
		return map[int]*slotCounts{}
	}
	defer rows.Close()

	out := make(map[int]*slotCounts)
	for rows.Next() {
		var idx int
		c := &slotCounts{}
		if err := rows.Scan(
			&idx, &c.total, &c.success, &c.failure, &c.formatError, &c.rateLimit, &c.empty,
			&c.timedRequests, &c.within5s, &c.within10s, &c.durationTimedRequests,
			&c.durationWithin10s, &c.durationWithin20s, &c.outputRequests, &c.claudeRequests,
			&c.cacheDenominatorSum, &c.cacheTokensSum, &c.cacheWriteSum,
			&c.cacheWriteTokensSum, &c.inputTokensSum, &c.outputTokensSum,
			&c.completionTokensSum, &c.quotaSum, &c.useTimeSum,
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

func buildHistoryChannelModelResult(date string, channelID int64, channelName string, modelName string, st *dailyPerfStats, slots map[int]*slotCounts) map[string]interface{} {
	if st == nil {
		st = &dailyPerfStats{}
	}
	perf := perfSummaryFromDaily(st)
	successRate := float64(100)
	if st.totalRequests > 0 {
		successRate = float64(st.successCount) / float64(st.totalRequests) * 100
	}
	rules := GetErrorRules()
	modelRate := modelSuccessRate(st.successCount, st.totalRequests, st.formatError, st.rateLimit, rules)
	modelStatus := getStatusColor(modelRate, modelAvailabilityDenominator(st.totalRequests, st.formatError, st.rateLimit, rules))

	return map[string]interface{}{
		"channel_id":               channelID,
		"channel_name":             channelName,
		"model_name":               modelName,
		"display_name":             modelName,
		"time_window":              "24h",
		"date":                     date,
		"total_requests":           st.totalRequests,
		"success_count":            st.successCount,
		"failure_count":            st.failureCount,
		"format_error_count":       st.formatError,
		"rate_limit_count":         st.rateLimit,
		"non_format_failure_count": nonFormatFailureCount(st.failureCount, st.formatError),
		"model_failure_count":      nonFormatFailureCount(st.failureCount, st.formatError),
		"model_error_count":        nonFormatFailureCount(st.failureCount, st.formatError) + st.emptyCount,
		"non_empty_count":          st.successCount,
		"empty_count":              st.emptyCount,
		"success_rate":             roundRate(successRate),
		"model_success_rate":       roundRate(modelRate),
		"model_availability_rate":  roundRate(modelRate),
		"current_status":           getStatusColor(successRate, st.totalRequests),
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
		"total_quota":              perf["total_quota"],
		"slot_data":                buildAvailabilitySlotData(slots, dayStartTimestamp(date), historySlotSeconds, historySlotCount),
	}
}

func (s *ModelHistoryService) GetChannelModelPerformanceByDate(date string, channelID int64, limit, offset int) (map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	channelName, channelFound, err := s.getHistoryChannelName(date, channelID)
	if err != nil {
		return nil, err
	}
	if !channelFound {
		channelName = fmt.Sprintf("Channel#%d", channelID)
		return map[string]interface{}{
			"date":         date,
			"time_window":  "24h",
			"channel_id":   channelID,
			"channel_name": channelName,
			"total":        0,
			"limit":        limit,
			"offset":       offset,
			"has_more":     false,
			"data":         []map[string]interface{}{},
		}, nil
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM model_daily_channel_model WHERE date = ? AND channel_id = ?`, date, channelID).Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, ErrHistoryChannelModelDetailNotBuilt
	}

	rows, err := s.db.Query(`SELECT model_name, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
		FROM model_daily_channel_model
		WHERE date = ? AND channel_id = ?
		ORDER BY total_requests DESC, model_name ASC
		LIMIT ? OFFSET ?`, date, channelID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 先把这一页的行读完并释放连接（SQLite MaxOpenConns=1），再逐个查 slot 明细，
	// 否则在 rows 未关闭时对同一连接发起 getChannelModelSlots 会死锁。
	type modelRow struct {
		name string
		st   *dailyPerfStats
	}
	ordered := make([]modelRow, 0)
	for rows.Next() {
		var modelName string
		st := &dailyPerfStats{}
		if err := rows.Scan(&modelName, &st.totalRequests, &st.successCount, &st.failureCount, &st.formatError, &st.rateLimit, &st.emptyCount, &st.timedRequests,
			&st.within5s, &st.within10s, &st.durationTimedRequests, &st.durationWithin10s, &st.durationWithin20s,
			&st.outputRequests, &st.claudeRequests, &st.cacheDenominatorSum, &st.cacheTokensSum, &st.cacheWriteSum,
			&st.cacheWriteTokensSum, &st.inputTokensSum, &st.outputTokensSum,
			&st.completionTokensSum, &st.quotaSum, &st.useTimeSum); err != nil {
			return nil, err
		}
		ordered = append(ordered, modelRow{name: modelName, st: st})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	data := make([]map[string]interface{}, 0, len(ordered))
	for _, item := range ordered {
		data = append(data, buildHistoryChannelModelResult(date, channelID, channelName, item.name, item.st, s.getChannelModelSlots(date, channelID, item.name)))
	}

	return map[string]interface{}{
		"date":         date,
		"time_window":  "24h",
		"channel_id":   channelID,
		"channel_name": channelName,
		"total":        total,
		"limit":        limit,
		"offset":       offset,
		"has_more":     offset+len(data) < total,
		"data":         data,
	}, nil
}

func (s *ModelHistoryService) getChannelSlots(date string, channelID int64) map[int]*slotCounts {
	rows, err := s.db.Query(`SELECT slot_idx, total_requests, success_count, failure_count, format_error_count, rate_limit_count, empty_count,
		timed_requests, within_5s, within_10s, duration_timed_requests,
		duration_within_10s, duration_within_20s, output_requests, claude_requests,
		cache_denominator_sum, cache_tokens_sum, cache_write_sum,
		cache_write_tokens_sum, input_tokens_sum, output_tokens_sum,
		completion_tokens_sum, quota_sum, use_time_sum
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
			&idx, &c.total, &c.success, &c.failure, &c.formatError, &c.rateLimit, &c.empty,
			&c.timedRequests, &c.within5s, &c.within10s, &c.durationTimedRequests,
			&c.durationWithin10s, &c.durationWithin20s, &c.outputRequests, &c.claudeRequests,
			&c.cacheDenominatorSum, &c.cacheTokensSum, &c.cacheWriteSum,
			&c.cacheWriteTokensSum, &c.inputTokensSum, &c.outputTokensSum,
			&c.completionTokensSum, &c.quotaSum, &c.useTimeSum,
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

// GetChannelCostTrends returns per-channel daily cost (quota) for a date range
// with optional week-over-week or month-over-month comparison.
func (s *ModelHistoryService) GetChannelCostTrends(days int, compareMode string) (map[string]interface{}, error) {
	// Daily channel snapshots are finalized after the day closes, so the
	// current comparison period must end yesterday rather than include a
	// partially populated (or absent) row for today.
	periodEnd := time.Now().AddDate(0, 0, -1)
	dates := channelCostDateRange(periodEnd, days)

	// Query all channel data for current dates
	currentByChannel, channelNames, err := s.queryChannelCostForDates(dates)
	if err != nil {
		return nil, err
	}

	if compareMode == "" {
		for channelID, data := range currentByChannel {
			currentByChannel[channelID] = alignChannelCostData(data, dates)
		}
		channels := s.buildChannelCostTrends(currentByChannel, channelNames, dates, nil, nil)
		return map[string]interface{}{"channels": channels}, nil
	}

	// Comparison mode
	offsetDays := 7
	if compareMode == "month" {
		offsetDays = 30
	}

	prevDates := channelCostDateRange(periodEnd.AddDate(0, 0, -offsetDays), days)

	prevByChannel, prevChannelNames, err := s.queryChannelCostForDates(prevDates)
	if err != nil {
		return nil, err
	}

	channelIDs := make(map[int64]struct{}, len(currentByChannel)+len(prevByChannel))
	for channelID := range currentByChannel {
		channelIDs[channelID] = struct{}{}
	}
	for channelID := range prevByChannel {
		channelIDs[channelID] = struct{}{}
		if channelNames[channelID] == "" {
			channelNames[channelID] = prevChannelNames[channelID]
		}
	}

	comparison := make(map[int64][]map[string]interface{})
	for chID := range channelIDs {
		curData := alignChannelCostData(currentByChannel[chID], dates)
		prevData := alignChannelCostData(prevByChannel[chID], prevDates)
		currentByChannel[chID] = curData
		prevByChannel[chID] = prevData
		comp := make([]map[string]interface{}, days)
		for i := 0; i < days; i++ {
			curVal := toInt64(curData[i]["total_quota"])
			prevVal := toInt64(prevData[i]["total_quota"])
			entry := map[string]interface{}{"date": dates[i]}
			if prevVal > 0 {
				entry["total_quota_change"] = math.Round(float64(curVal-prevVal)/float64(prevVal)*10000) / 100
			}
			comp[i] = entry
		}
		comparison[chID] = comp
	}

	channels := s.buildChannelCostTrends(currentByChannel, channelNames, dates, prevByChannel, comparison)

	modeLabel := "week_over_week"
	if compareMode == "month" {
		modeLabel = "month_over_month"
	}

	return map[string]interface{}{
		"channels":       channels,
		"compare_mode":   modeLabel,
		"compare_offset": offsetDays,
	}, nil
}

func channelCostDateRange(end time.Time, days int) []string {
	if days <= 0 {
		return []string{}
	}
	dates := make([]string, days)
	for i := 0; i < days; i++ {
		dates[i] = end.AddDate(0, 0, -days+1+i).Format("2006-01-02")
	}
	return dates
}

func alignChannelCostData(data []map[string]interface{}, dates []string) []map[string]interface{} {
	byDate := make(map[string]map[string]interface{}, len(data))
	for _, item := range data {
		if date, ok := item["date"].(string); ok {
			byDate[date] = item
		}
	}

	aligned := make([]map[string]interface{}, len(dates))
	for i, date := range dates {
		if item, ok := byDate[date]; ok {
			aligned[i] = item
			continue
		}
		aligned[i] = map[string]interface{}{
			"date":           date,
			"total_quota":    int64(0),
			"total_tokens":   int64(0),
			"total_requests": int64(0),
		}
	}
	return aligned
}

// queryChannelCostForDates queries model_daily_channel for a list of dates
// and returns data keyed by channel_id, plus channel name mapping.
func (s *ModelHistoryService) queryChannelCostForDates(dates []string) (map[int64][]map[string]interface{}, map[int64]string, error) {
	if len(dates) == 0 {
		return nil, nil, nil
	}

	placeholders := make([]string, len(dates))
	args := make([]interface{}, len(dates))
	for i, d := range dates {
		placeholders[i] = "?"
		args[i] = d
	}

	query := fmt.Sprintf(`SELECT date, channel_id, channel_name,
			quota_sum as total_quota,
			COALESCE(input_tokens_sum, 0) + COALESCE(output_tokens_sum, 0) as total_tokens,
			total_requests
		FROM model_daily_channel
		WHERE date IN (%s)
		ORDER BY channel_id, date ASC`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	byChannel := make(map[int64][]map[string]interface{})
	channelNames := make(map[int64]string)

	for rows.Next() {
		var date, channelName string
		var channelID, totalRequests int64
		var quota, totalTokens float64
		if err := rows.Scan(&date, &channelID, &channelName, &quota, &totalTokens, &totalRequests); err != nil {
			return nil, nil, err
		}
		byChannel[channelID] = append(byChannel[channelID], map[string]interface{}{
			"date":           date,
			"total_quota":    int64(math.Round(quota)),
			"total_tokens":   int64(math.Round(totalTokens)),
			"total_requests": totalRequests,
		})
		if _, ok := channelNames[channelID]; !ok && channelName != "" {
			channelNames[channelID] = channelName
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return byChannel, channelNames, nil
}

// buildChannelCostTrends builds the per-channel cost trend response.
func (s *ModelHistoryService) buildChannelCostTrends(
	currentByChannel map[int64][]map[string]interface{},
	channelNames map[int64]string,
	dates []string,
	prevByChannel map[int64][]map[string]interface{},
	comparison map[int64][]map[string]interface{},
) []map[string]interface{} {
	channels := make([]map[string]interface{}, 0, len(currentByChannel))
	for chID, curData := range currentByChannel {
		name := channelNames[chID]
		if name == "" {
			name = fmt.Sprintf("Channel#%d", chID)
		}
		entry := map[string]interface{}{
			"channel_id":   chID,
			"channel_name": name,
			"current":      curData,
		}
		if prevByChannel != nil {
			entry["previous"] = prevByChannel[chID]
		}
		if comparison != nil {
			entry["comparison"] = comparison[chID]
		}
		channels = append(channels, entry)
	}
	// Sort by total cost descending
	sort.Slice(channels, func(i, j int) bool {
		sumI := int64(0)
		for _, d := range channels[i]["current"].([]map[string]interface{}) {
			sumI += toInt64(d["total_quota"])
		}
		sumJ := int64(0)
		for _, d := range channels[j]["current"].([]map[string]interface{}) {
			sumJ += toInt64(d["total_quota"])
		}
		return sumI > sumJ
	})
	return channels
}

// QueryDailyAggregatedTrends returns daily Token/cache trends aggregated across
// all channels, using the day_group format expected by fillDailyGaps.
//
// This avoids scanning the main logs/quota_data tables for the 28-day weekly
// pattern view, which is a heavy real-time aggregation. The model_history.db
// already pre-aggregates per-channel-per-day data daily at 01:00 local time.
//
// Only completed days are returned; callers merge today's live aggregate.
func (s *ModelHistoryService) QueryDailyAggregatedTrends(days int, tzOffset int64) ([]map[string]interface{}, error) {
	cutoff := time.Now().AddDate(0, 0, -days+1).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")

	query := `SELECT date,
			COALESCE(SUM(input_tokens_sum), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens_sum), 0) as completion_tokens,
			COALESCE(SUM(cache_tokens_sum), 0) as cache_hit_tokens,
			COALESCE(SUM(cache_write_tokens_sum), 0) as cache_write_tokens
		FROM model_daily_channel
		WHERE date >= ? AND date < ?
		GROUP BY date
		ORDER BY date ASC`

	rows, err := s.db.Query(query, cutoff, today)
	if err != nil {
		return nil, fmt.Errorf("query model_daily_channel trends: %w", err)
	}
	defer rows.Close()

	loc := time.Now().Location()
	result := make([]map[string]interface{}, 0, days)

	for rows.Next() {
		var dateStr string
		var promptTokens, completionTokens, cacheHitTokens, cacheWriteTokens float64
		if err := rows.Scan(&dateStr, &promptTokens, &completionTokens, &cacheHitTokens, &cacheWriteTokens); err != nil {
			return nil, fmt.Errorf("scan model_daily_channel row: %w", err)
		}

		// Compute day_group compatible with fillDailyGaps:
		//   dayStart := time.Date(year, month, day, 0,0,0,0, loc)
		//   day_group = (dayStart.Unix() + tzOffset) / 86400
		t, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			continue // skip unparseable dates
		}
		dayGroup := (t.Unix() + tzOffset) / 86400

		result = append(result, map[string]interface{}{
			"day_group":          dayGroup,
			"prompt_tokens":      int64(promptTokens),
			"completion_tokens":  int64(completionTokens),
			"cache_hit_tokens":   int64(cacheHitTokens),
			"cache_write_tokens": int64(cacheWriteTokens),
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model_daily_channel rows: %w", err)
	}
	return result, nil
}
