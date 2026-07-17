package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/auth"
	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/config"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/handler"
	"github.com/new-api-tools/backend/internal/logger"
	"github.com/new-api-tools/backend/internal/middleware"
	"github.com/new-api-tools/backend/internal/service"
)

func main() {
	// ========== 1. Load configuration ==========
	cfg := config.Load()

	// ========== 2. Initialize logger ==========
	logger.Init(cfg.LogLevel, cfg.LogFile)
	logger.L.Banner("🚀 NewAPI Middleware Tool - Go Backend")
	logger.L.System(fmt.Sprintf("服务器地址: %s", cfg.ServerAddr()))
	logger.L.System(fmt.Sprintf("数据库引擎: %s", cfg.DatabaseEngine))
	logger.L.System(fmt.Sprintf("时区: %s", cfg.TimeZone))

	// ========== 3. Initialize database ==========
	_, err := database.Init(cfg)
	if err != nil {
		logger.L.Fatal("数据库初始化失败: " + err.Error())
	}
	defer database.Close()

	// Ensure indexes (background, with delay to reduce load)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.L.Error(fmt.Sprintf("索引创建 goroutine panic: %v", r))
			}
		}()
		time.Sleep(2 * time.Second)
		db := database.Get()
		db.EnsureIndexes(true, 500*time.Millisecond)
	}()

	// ========== 4. Initialize Redis cache ==========
	if cfg.RedisConnString != "" {
		_, err := cache.Init(cfg.RedisConnString)
		if err != nil {
			logger.L.Warn("Redis 连接失败，将使用无缓存模式: " + err.Error())
		}
	} else {
		logger.L.Warn("REDIS_CONN_STRING 未配置，缓存功能不可用")
	}
	defer cache.Close()

	// ========== 5. Setup Gin router ==========
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// Global middleware
	r.Use(middleware.ErrorHandlerMiddleware())  // Panic recovery
	r.Use(middleware.CORSMiddleware())          // CORS
	r.Use(middleware.RequestLoggerMiddleware()) // Request logging

	// ========== 6. Register routes ==========

	// Health check (no auth required)
	handler.RegisterHealthRoutes(r)

	// API group with authentication
	api := r.Group("/api")
	api.Use(auth.AuthMiddleware())
	{
		// Auth routes (login/logout are whitelisted in middleware)
		handler.RegisterAuthRoutes(api)

		// Phase 2.1: Basic modules
		handler.RegisterRedemptionRoutes(api)
		handler.RegisterTopUpRoutes(api)
		handler.RegisterStorageRoutes(api)
		handler.RegisterSystemRoutes(api)

		// Phase 2.2: Dashboard, UserManagement, LogAnalytics
		handler.RegisterDashboardRoutes(api)
		handler.RegisterUserManagementRoutes(api)
		handler.RegisterLogAnalyticsRoutes(api)

		// Phase 2.3: IP Monitoring, Risk Monitoring, Model Status
		handler.RegisterIPMonitoringRoutes(api)
		handler.RegisterRiskMonitoringRoutes(api)
		handler.RegisterModelStatusRoutes(api)

		// Phase 2.4: Token Management
		handler.RegisterTokenRoutes(api)

		// Phase 3: AI AutoBan, AutoGroup, LinuxDo Lookup
		handler.RegisterAIAutoBanRoutes(api)
		handler.RegisterAutoGroupRoutes(api)
		handler.RegisterLinuxDoRoutes(api)
	}

	// Public embed routes (no auth)
	handler.RegisterModelStatusEmbedRoutes(r)

	// ========== 7. Background tasks ==========

	// IP recording enforcement: check every 10 minutes, enable if any user disabled it
	stopIPEnforce := make(chan struct{})
	go backgroundEnforceIPRecording(stopIPEnforce)

	// 模型监控历史: 初始化 SQLite 历史库, 后台补拉全部已结束日期, 每天凌晨 1 点统计前一天
	stopModelHistory := make(chan struct{})
	go backgroundModelHistory(stopModelHistory)

	// ========== 8. Start server with graceful shutdown ==========
	srv := &http.Server{
		Addr:         cfg.ServerAddr(),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in goroutine
	go func() {
		logger.L.Success(fmt.Sprintf("服务已启动: http://%s", cfg.ServerAddr()))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.L.Fatal("服务启动失败: " + err.Error())
		}
	}()

	// ========== 9. Wait for interrupt signal ==========
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.L.System("正在优雅关闭服务...")

	// Stop background tasks
	close(stopIPEnforce)
	close(stopModelHistory)

	// Give the server 10 seconds to finish processing requests
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.L.Error("服务关闭异常: " + err.Error())
	}

	logger.L.Success("服务已关闭")
}

// backgroundEnforceIPRecording periodically checks and enforces IP recording for all users.
func backgroundEnforceIPRecording(stop <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error(fmt.Sprintf("[IP记录] 后台任务 panic: %v", r))
		}
	}()

	// Wait 30 seconds after startup before first check
	select {
	case <-time.After(30 * time.Second):
	case <-stop:
		return
	}

	logger.L.System("[IP记录] 强制开启定时任务已启动 (间隔: 10分钟)")

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// Run immediately on first tick, then every 10 minutes
	for {
		enforceIPRecordingOnce()

		select {
		case <-ticker.C:
		case <-stop:
			logger.L.System("[IP记录] 强制开启定时任务已停止")
			return
		}
	}
}

func enforceIPRecordingOnce() {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error(fmt.Sprintf("[IP记录] 检查执行 panic: %v", r))
		}
	}()

	svc := service.NewIPMonitoringService()

	stats, err := svc.GetIPStats()
	if err != nil {
		logger.L.Warn("[IP记录] 获取状态失败: " + err.Error())
		return
	}

	disabledCount := toInt64(stats["disabled_count"])
	totalUsers := toInt64(stats["total_users"])

	if disabledCount == 0 {
		logger.L.Debug(fmt.Sprintf("[IP记录] 所有用户 (%d) 已开启 IP 记录，无需操作", totalUsers))
		return
	}

	logger.L.System(fmt.Sprintf("[IP记录] 检测到 %d 个用户关闭了 IP 记录，正在强制开启...", disabledCount))

	result, err := svc.EnableAllIPRecording()
	if err != nil {
		logger.L.Warn("[IP记录] 强制开启失败: " + err.Error())
		return
	}

	logger.L.Success(fmt.Sprintf("[IP记录] %s", result["message"]))
}

func toInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case int64:
		return val
	case int:
		return int64(val)
	case float64:
		return int64(val)
	default:
		return 0
	}
}

// backgroundModelHistory initializes the SQLite history store, starts a
// resumable rebuild of every completed source-log day, and aggregates the
// just-finished day at ~01:00 local time each day.
func backgroundModelHistory(stop <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error(fmt.Sprintf("[模型历史] 后台任务 panic: %v", r))
		}
	}()

	// Wait a bit after startup so DB/indexes settle before the first scan.
	select {
	case <-time.After(15 * time.Second):
	case <-stop:
		return
	}

	hist, err := service.GetModelHistoryService()
	if err != nil {
		logger.L.Error("[模型历史] 历史库初始化失败: " + err.Error())
		return
	}

	// Rebuild every completed source-log day once, then only fill gaps on later
	// startups. The worker is independent from HTTP startup and shares the daily
	// aggregation mutex with the 01:00 scheduler.
	go backfillAllModelHistory(hist, stop)

	logger.L.System("[模型历史] 定时统计任务已启动 (每天 01:00 统计前一天)")

	for {
		now := time.Now().In(time.Local)
		// Next run at 01:00 local time.
		next := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, time.Local)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		wait := time.Until(next)

		select {
		case <-time.After(wait):
			day := time.Now().In(time.Local).AddDate(0, 0, -1).Format("2006-01-02")
			logger.L.System("[模型历史] 开始统计前一天数据: " + day)
			_ = aggregateModelHistoryDay(day)
		case <-stop:
			logger.L.System("[模型历史] 定时统计任务已停止")
			return
		}
	}
}

var modelHistoryAggregationMu sync.Mutex

func aggregateModelHistoryDay(date string) (err error) {
	modelHistoryAggregationMu.Lock()
	defer modelHistoryAggregationMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error(fmt.Sprintf("[模型历史] 统计 %s panic: %v", date, r))
			err = fmt.Errorf("aggregate model history %s panic: %v", date, r)
		}
	}()
	svc := service.NewModelStatusService()
	if aggregateErr := svc.AggregateDay(date); aggregateErr != nil {
		logger.L.Warn(fmt.Sprintf("[模型历史] 统计 %s 失败: %s", date, aggregateErr.Error()))
		return aggregateErr
	}
	logger.L.Success("[模型历史] 统计完成: " + date)
	return nil
}

func completedHistoryDatesNewestFirst(oldest, now time.Time) []string {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	oldest = oldest.In(time.Local)
	oldest = time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, time.Local)
	if !oldest.Before(today) {
		return []string{}
	}

	dates := make([]string, 0, int(today.Sub(oldest).Hours()/24))
	for day := today.AddDate(0, 0, -1); !day.Before(oldest); day = day.AddDate(0, 0, -1) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	return dates
}

// backfillAllModelHistory rebuilds every completed local date covered by the
// main logs table. The first v2 run deliberately rebuilds existing snapshots;
// later startups only fill dates missing the current daily-totals record.
func backfillAllModelHistory(hist *service.ModelHistoryService, stop <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error(fmt.Sprintf("[模型历史] 全量回填 panic: %v", r))
		}
	}()

	svc := service.NewModelStatusService()
	oldest, found, err := svc.OldestPerformanceLogDate()
	if err != nil {
		logger.L.Warn("[模型历史] 查询最早日志日期失败: " + err.Error())
		return
	}
	if !found {
		if err := hist.MarkFullHistoryBackfillComplete(); err != nil {
			logger.L.Warn("[模型历史] 标记全量回填完成失败: " + err.Error())
			return
		}
		logger.L.System("[模型历史] 主库没有可聚合日志，无需历史回填")
		return
	}

	allDates := completedHistoryDatesNewestFirst(oldest, time.Now().In(time.Local))
	fullComplete, err := hist.IsFullHistoryBackfillComplete()
	if err != nil {
		logger.L.Warn("[模型历史] 检查全量回填状态失败: " + err.Error())
		return
	}

	pending := make([]string, 0, len(allDates))
	for _, date := range allDates {
		var done bool
		if fullComplete {
			done, err = hist.HasDailyTotals(date)
		} else {
			done, err = hist.IsFullHistoryBackfillDateComplete(date)
		}
		if err != nil {
			logger.L.Warn(fmt.Sprintf("[模型历史] 检查回填日期 %s 失败: %s", date, err.Error()))
			return
		}
		if !done {
			pending = append(pending, date)
		}
	}

	if len(pending) == 0 {
		if !fullComplete {
			if err := hist.MarkFullHistoryBackfillComplete(); err != nil {
				logger.L.Warn("[模型历史] 标记全量回填完成失败: " + err.Error())
				return
			}
		}
		logger.L.System("[模型历史] 所有已结束日期均已聚合，无需补拉")
		return
	}

	mode := "缺失日期补拉"
	if !fullComplete {
		mode = "首次全量重建"
	}
	logger.L.System(fmt.Sprintf("[模型历史] %s: 待处理 %d/%d 天，范围 %s 至 %s，按日期从新到旧(间隔2.5min)",
		mode, len(pending), len(allDates), pending[len(pending)-1], pending[0]))
	const interval = 2*time.Minute + 30*time.Second

	for i, date := range pending {
		select {
		case <-stop:
			logger.L.System("[模型历史] 全量回填收到停止信号，已中断")
			return
		default:
		}

		logger.L.System(fmt.Sprintf("[模型历史] [%d/%d] 补拉日期: %s", i+1, len(pending), date))
		if err := aggregateModelHistoryDay(date); err != nil {
			logger.L.Warn(fmt.Sprintf("[模型历史] 补拉失败，将在下次启动重试: %s", date))
			return
		}
		if err := hist.MarkFullHistoryBackfillDateComplete(date); err != nil {
			logger.L.Warn(fmt.Sprintf("[模型历史] 记录补拉进度失败 %s: %s", date, err.Error()))
			return
		}

		if i < len(pending)-1 {
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
		}
	}
	if !fullComplete {
		if err := hist.MarkFullHistoryBackfillComplete(); err != nil {
			logger.L.Warn("[模型历史] 标记全量回填完成失败: " + err.Error())
			return
		}
	}
	logger.L.Success("[模型历史] 所有历史日期补拉完成")
}
