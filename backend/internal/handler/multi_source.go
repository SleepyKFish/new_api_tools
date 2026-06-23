package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterMultiSourceRoutes 注册多源监控的「管理端」接口 (需鉴权)。
func RegisterMultiSourceRoutes(r *gin.RouterGroup) {
	g := r.Group("/multi-source")
	{
		// 源管理
		g.GET("/sources", GetMonitorSources)
		g.POST("/sources", UpsertMonitorSource)
		g.PUT("/sources/:id", UpsertMonitorSource)
		g.DELETE("/sources/:id", DeleteMonitorSource)
		g.PUT("/sources/:id/models", SetMonitorSourceModels)
		g.GET("/sources/:id/available-models", GetMonitorSourceAvailableModels)

		// 聚合配置
		g.GET("/config", GetMultiSourceConfig)
		g.PUT("/config", SetMultiSourceConfig)
		g.POST("/config", SetMultiSourceConfig)

		// 错误分类规则
		g.GET("/error-rules", GetMultiSourceErrorRules)
		g.PUT("/error-rules", SetMultiSourceErrorRules)
		g.POST("/error-rules", SetMultiSourceErrorRules)

		// 管理端预览聚合结果 (强制不走缓存)
		g.GET("/aggregated", func(c *gin.Context) { getAggregated(c, true) })
	}
}

// RegisterMultiSourceEmbedRoutes 注册「公开端」接口 (无鉴权)。
//   - leaf: /api/embed/model-status/classified/batch —— 供其它部署联邦拉取本机分类数据
//   - 聚合: /api/embed/multi-source/{config,aggregated} —— 公开监控页数据
func RegisterMultiSourceEmbedRoutes(r *gin.Engine) {
	// leaf 接口: 返回本机已分类的模型状态，挂在现有 embed 命名空间下
	for _, base := range []string{"/api/embed/model-status", "/api/model-status/embed"} {
		grp := r.Group(base)
		grp.POST("/classified/batch", GetClassifiedModelsStatusHandler)
		grp.POST("/classified/multiple", GetClassifiedModelsStatusHandler)
	}

	// 聚合公开页接口
	for _, base := range []string{"/api/embed/multi-source", "/api/multi-source/embed"} {
		grp := r.Group(base)
		grp.GET("/config", GetMultiSourceEmbedConfig)
		grp.GET("/aggregated", func(c *gin.Context) { getAggregated(c, false) })
	}
}

// ========== leaf: 本机分类数据 (公开) ==========

// GetClassifiedModelsStatusHandler 返回带错误分类字段的本机模型状态。
// 既供本机聚合直接调用，也供远程聚合端通过 HTTP 联邦拉取。
func GetClassifiedModelsStatusHandler(c *gin.Context) {
	var modelNames []string
	if err := c.ShouldBindJSON(&modelNames); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Expected array of model names", err.Error()))
		return
	}
	window := c.DefaultQuery("window", service.DefaultTimeWindow)

	svc := service.NewModelStatusService()
	data, err := svc.GetClassifiedModelsStatus(modelNames, window)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"data":        data,
		"time_window": window,
	})
}

// ========== 源管理 ==========

// GET /multi-source/sources
func GetMonitorSources(c *gin.Context) {
	svc := service.NewMultiSourceService()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": svc.GetSources()})
}

// POST /multi-source/sources  或  PUT /multi-source/sources/:id
func UpsertMonitorSource(c *gin.Context) {
	var src service.MonitorSource
	if err := c.ShouldBindJSON(&src); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid source payload", err.Error()))
		return
	}
	if id := c.Param("id"); id != "" {
		src.ID = id
	}
	svc := service.NewMultiSourceService()
	saved, err := svc.UpsertSource(src)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_SOURCE", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": saved})
}

// DELETE /multi-source/sources/:id
func DeleteMonitorSource(c *gin.Context) {
	id := c.Param("id")
	svc := service.NewMultiSourceService()
	if err := svc.DeleteSource(id); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("DELETE_FAILED", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// PUT /multi-source/sources/:id/models
func SetMonitorSourceModels(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Models []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request", err.Error()))
		return
	}
	svc := service.NewMultiSourceService()
	if err := svc.SetSourceModels(id, req.Models); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("UPDATE_FAILED", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GET /multi-source/sources/:id/available-models
func GetMonitorSourceAvailableModels(c *gin.Context) {
	id := c.Param("id")
	svc := service.NewMultiSourceService()
	data, err := svc.GetSourceAvailableModels(id)
	if err != nil {
		c.JSON(http.StatusBadGateway, models.ErrorResp("FETCH_FAILED", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// ========== 聚合配置 ==========

// GET /multi-source/config
func GetMultiSourceConfig(c *gin.Context) {
	svc := service.NewMultiSourceService()
	c.JSON(http.StatusOK, gin.H{
		"success":                     true,
		"data":                        svc.GetConfig(),
		"available_time_windows":      service.AvailableTimeWindows,
		"available_themes":            service.AvailableThemes,
		"available_refresh_intervals": service.AvailableRefreshIntervals,
	})
}

// PUT/POST /multi-source/config
func SetMultiSourceConfig(c *gin.Context) {
	var cfg service.MultiSourceConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid config", err.Error()))
		return
	}
	svc := service.NewMultiSourceService()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": svc.SetConfig(cfg)})
}

// ========== 错误分类规则 ==========

// GET /multi-source/error-rules
func GetMultiSourceErrorRules(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    service.GetErrorRules(),
		"default": service.DefaultErrorRules(),
	})
}

// PUT/POST /multi-source/error-rules
func SetMultiSourceErrorRules(c *gin.Context) {
	var rules service.ErrorRuleConfig
	if err := c.ShouldBindJSON(&rules); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid rules", err.Error()))
		return
	}
	service.SetErrorRules(rules)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": service.GetErrorRules()})
}

// ========== 聚合结果 ==========

func getAggregated(c *gin.Context, forceNoCache bool) {
	noCache := forceNoCache || c.Query("no_cache") == "true"
	svc := service.NewMultiSourceService()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": svc.GetAggregated(noCache)})
}

// ========== 公开页配置 ==========

// GET /embed/multi-source/config
func GetMultiSourceEmbedConfig(c *gin.Context) {
	svc := service.NewMultiSourceService()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": svc.GetEmbedConfig()})
}
