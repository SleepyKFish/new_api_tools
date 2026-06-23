package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
)

// 多源模型监控: 把「本机源」(直连本地 NewAPI DB) 与若干「远程源」(同款工具的其它
// 部署，通过其公开 embed 接口拉取已算好的数据) 聚合成一个统一的公开监控页。
//
// 远程源采用 HTTP 联邦: 聚合端调用对端的「leaf」分类接口
//
//	POST {base_url}/api/embed/model-status/classified/batch?window=xx
//
// 拿到对端本机已分类好的模型状态，再打上源标签合并。无需对端数据库凭据。

const (
	SourceTypeLocal      = "local"
	SourceTypeRemoteHTTP = "remote_http"

	multiSourceSourcesKey = "multi_source:sources"
	multiSourceConfigKey  = "multi_source:config"
	multiSourceAggPrefix  = "multi_source:agg:" // 聚合结果短 TTL 缓存前缀

	// 远程拉取的超时与聚合结果缓存 TTL
	remoteFetchTimeout = 12 * time.Second
)

// MonitorSource 描述一个信息源。
type MonitorSource struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`              // local | remote_http
	BaseURL string   `json:"base_url"`          // remote_http 必填，如 https://peer.example.com
	Enabled bool     `json:"enabled"`           // 是否参与聚合
	Models  []string `json:"models"`            // 该源要展示的模型 (空=不展示)
	Token   string   `json:"token,omitempty"`   // 可选: 远程源需要的访问令牌 (Bearer)
}

// MultiSourceConfig 是聚合页的全局配置 (热更新)。
type MultiSourceConfig struct {
	TimeWindow      string `json:"time_window"`      // 仅展示最近 xxmin: 时间窗口
	RefreshInterval int    `json:"refresh_interval"` // xxmin 更新一次: 前端自动刷新秒数
	Title           string `json:"title"`            // 公开页标题
	Theme           string `json:"theme"`            // 主题 (复用现有主题集)
	AggCacheTTL     int    `json:"agg_cache_ttl"`    // 聚合结果服务端缓存秒数 (默认=refresh/2)
}

// MultiSourceService 管理源配置与聚合。
type MultiSourceService struct {
	httpClient *http.Client
}

// NewMultiSourceService 创建服务实例。
func NewMultiSourceService() *MultiSourceService {
	return &MultiSourceService{
		httpClient: &http.Client{Timeout: remoteFetchTimeout},
	}
}

// ========== 配置读写 (Redis, TTL=0, 每请求读取实现热更新) ==========

// DefaultMultiSourceConfig 返回默认聚合配置。
func DefaultMultiSourceConfig() MultiSourceConfig {
	return MultiSourceConfig{
		TimeWindow:      "15m",
		RefreshInterval: 60,
		Title:           "模型监控",
		Theme:           DefaultTheme,
		AggCacheTTL:     30,
	}
}

// GetConfig 读取聚合配置，未配置返回默认值。
func (s *MultiSourceService) GetConfig() MultiSourceConfig {
	cm := cache.Get()
	var cfg MultiSourceConfig
	found, _ := cm.GetJSON(multiSourceConfigKey, &cfg)
	if !found {
		return DefaultMultiSourceConfig()
	}
	def := DefaultMultiSourceConfig()
	if cfg.TimeWindow == "" || !IsValidTimeWindow(cfg.TimeWindow) {
		cfg.TimeWindow = def.TimeWindow
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = def.RefreshInterval
	}
	if cfg.Title == "" {
		cfg.Title = def.Title
	}
	if cfg.Theme == "" {
		cfg.Theme = def.Theme
	}
	if mapped, ok := LegacyThemeMap[cfg.Theme]; ok {
		cfg.Theme = mapped
	}
	if cfg.AggCacheTTL <= 0 {
		cfg.AggCacheTTL = max(5, cfg.RefreshInterval/2)
	}
	return cfg
}

// SetConfig 持久化聚合配置 (规范化时间窗口)。
func (s *MultiSourceService) SetConfig(cfg MultiSourceConfig) MultiSourceConfig {
	if cfg.TimeWindow != "" {
		cfg.TimeWindow = NormalizeTimeWindow(cfg.TimeWindow)
	}
	cm := cache.Get()
	cm.Set(multiSourceConfigKey, cfg, 0)
	// 改了窗口/缓存策略，清掉旧聚合缓存
	cm.DeleteByPrefix(multiSourceAggPrefix)
	return s.GetConfig()
}

// ========== 源 CRUD ==========

// GetSources 返回全部源 (含本机源)。本机源若不存在则注入一个默认本机源。
func (s *MultiSourceService) GetSources() []MonitorSource {
	cm := cache.Get()
	var sources []MonitorSource
	cm.GetJSON(multiSourceSourcesKey, &sources)
	if sources == nil {
		sources = []MonitorSource{}
	}
	// 确保始终存在一个本机源 (id 固定为 "local")
	hasLocal := false
	for _, src := range sources {
		if src.Type == SourceTypeLocal {
			hasLocal = true
			break
		}
	}
	if !hasLocal {
		sources = append([]MonitorSource{{
			ID:      "local",
			Name:    "本机",
			Type:    SourceTypeLocal,
			Enabled: true,
			Models:  []string{},
		}}, sources...)
	}
	return sources
}

func (s *MultiSourceService) saveSources(sources []MonitorSource) {
	cm := cache.Get()
	cm.Set(multiSourceSourcesKey, sources, 0)
	cm.DeleteByPrefix(multiSourceAggPrefix)
}

// UpsertSource 新增或更新一个源 (按 id 匹配；无 id 视为新增并生成 id)。
func (s *MultiSourceService) UpsertSource(src MonitorSource) (MonitorSource, error) {
	if err := validateSource(&src); err != nil {
		return src, err
	}
	sources := s.GetSources()
	if src.ID == "" {
		src.ID = generateSourceID(sources)
		sources = append(sources, src)
	} else {
		found := false
		for i := range sources {
			if sources[i].ID == src.ID {
				// 本机源类型不可改
				if sources[i].Type == SourceTypeLocal {
					src.Type = SourceTypeLocal
				}
				sources[i] = src
				found = true
				break
			}
		}
		if !found {
			sources = append(sources, src)
		}
	}
	s.saveSources(sources)
	return src, nil
}

// DeleteSource 删除一个源 (本机源不可删，只能停用)。
func (s *MultiSourceService) DeleteSource(id string) error {
	if id == "local" {
		return fmt.Errorf("本机源不可删除，可将其停用")
	}
	sources := s.GetSources()
	out := make([]MonitorSource, 0, len(sources))
	for _, src := range sources {
		if src.ID == id {
			if src.Type == SourceTypeLocal {
				return fmt.Errorf("本机源不可删除，可将其停用")
			}
			continue
		}
		out = append(out, src)
	}
	s.saveSources(out)
	return nil
}

// SetSourceModels 设置某个源要展示的模型列表。
func (s *MultiSourceService) SetSourceModels(id string, models []string) error {
	sources := s.GetSources()
	for i := range sources {
		if sources[i].ID == id {
			sources[i].Models = models
			s.saveSources(sources)
			return nil
		}
	}
	return fmt.Errorf("源不存在: %s", id)
}

// GetSourceAvailableModels 返回某个源可选的模型列表 (供后台「添加 xx源的xx模型」用)。
//   - 本机源: 查本地 abilities/logs
//   - 远程源: 调用对端 /api/embed/model-status/models
func (s *MultiSourceService) GetSourceAvailableModels(id string) ([]map[string]interface{}, error) {
	sources := s.GetSources()
	for _, src := range sources {
		if src.ID != id {
			continue
		}
		if src.Type == SourceTypeLocal {
			return NewModelStatusService().GetAvailableModels()
		}
		return s.fetchRemoteModels(src)
	}
	return nil, fmt.Errorf("源不存在: %s", id)
}

// ========== 聚合 ==========

// AggregatedResult 是公开页拿到的聚合输出。
type AggregatedResult struct {
	Sources    []AggregatedSource `json:"sources"`
	TimeWindow string             `json:"time_window"`
	Config     MultiSourceConfig  `json:"config"`
	UpdatedAt  int64              `json:"updated_at"`
}

// AggregatedSource 是单个源在聚合结果里的一段。
type AggregatedSource struct {
	SourceID   string                   `json:"source_id"`
	SourceName string                   `json:"source_name"`
	SourceType string                   `json:"source_type"`
	Models     []map[string]interface{} `json:"models"`
	Error      string                   `json:"error,omitempty"` // 该源拉取失败时的原因
}

// GetAggregated 聚合所有启用源的已分类模型状态，带服务端短 TTL 缓存。
func (s *MultiSourceService) GetAggregated(noCache bool) AggregatedResult {
	cfg := s.GetConfig()
	window := cfg.TimeWindow
	cacheKey := multiSourceAggPrefix + window

	cm := cache.Get()
	if !noCache {
		var cached AggregatedResult
		if found, _ := cm.GetJSON(cacheKey, &cached); found {
			return cached
		}
	}

	result := AggregatedResult{
		TimeWindow: window,
		Config:     cfg,
		UpdatedAt:  time.Now().Unix(),
		Sources:    []AggregatedSource{},
	}

	for _, src := range s.GetSources() {
		if !src.Enabled || len(src.Models) == 0 {
			continue
		}
		seg := AggregatedSource{
			SourceID:   src.ID,
			SourceName: src.Name,
			SourceType: src.Type,
			Models:     []map[string]interface{}{},
		}
		var (
			models []map[string]interface{}
			err    error
		)
		if src.Type == SourceTypeLocal {
			models, err = NewModelStatusService().GetClassifiedModelsStatus(src.Models, window)
		} else {
			models, err = s.fetchRemoteClassified(src, window)
		}
		if err != nil {
			seg.Error = err.Error()
		} else {
			for _, m := range models {
				m["source_id"] = src.ID
				m["source_name"] = src.Name
				seg.Models = append(seg.Models, m)
			}
		}
		result.Sources = append(result.Sources, seg)
	}

	cm.Set(cacheKey, result, time.Duration(cfg.AggCacheTTL)*time.Second)
	return result
}

// ========== 远程拉取 ==========

func (s *MultiSourceService) fetchRemoteClassified(src MonitorSource, window string) ([]map[string]interface{}, error) {
	url := strings.TrimRight(src.BaseURL, "/") + "/api/embed/model-status/classified/batch?window=" + window
	body, _ := json.Marshal(src.Models)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if src.Token != "" {
		req.Header.Set("Authorization", "Bearer "+src.Token)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("远程源不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("远程源返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("远程源响应解析失败: %w", err)
	}
	if parsed.Data == nil {
		return []map[string]interface{}{}, nil
	}
	return parsed.Data, nil
}

func (s *MultiSourceService) fetchRemoteModels(src MonitorSource) ([]map[string]interface{}, error) {
	url := strings.TrimRight(src.BaseURL, "/") + "/api/embed/model-status/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if src.Token != "" {
		req.Header.Set("Authorization", "Bearer "+src.Token)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("远程源不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("远程源返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Data == nil {
		return []map[string]interface{}{}, nil
	}
	return parsed.Data, nil
}

// ========== 公开页配置 ==========

// GetEmbedConfig 返回公开页所需的配置 + 可选项。
func (s *MultiSourceService) GetEmbedConfig() map[string]interface{} {
	cfg := s.GetConfig()
	return map[string]interface{}{
		"time_window":               cfg.TimeWindow,
		"refresh_interval":          cfg.RefreshInterval,
		"title":                     cfg.Title,
		"theme":                     cfg.Theme,
		"available_time_windows":    AvailableTimeWindows,
		"available_themes":          AvailableThemes,
		"available_refresh_intervals": AvailableRefreshIntervals,
	}
}

// ========== 辅助 ==========

func validateSource(src *MonitorSource) error {
	src.Name = strings.TrimSpace(src.Name)
	if src.Name == "" {
		return fmt.Errorf("源名称不能为空")
	}
	if src.Type == "" {
		src.Type = SourceTypeRemoteHTTP
	}
	if src.Type != SourceTypeLocal && src.Type != SourceTypeRemoteHTTP {
		return fmt.Errorf("未知源类型: %s", src.Type)
	}
	if src.Type == SourceTypeRemoteHTTP {
		src.BaseURL = strings.TrimSpace(src.BaseURL)
		if src.BaseURL == "" {
			return fmt.Errorf("远程源必须填写 base_url")
		}
		if !strings.HasPrefix(src.BaseURL, "http://") && !strings.HasPrefix(src.BaseURL, "https://") {
			return fmt.Errorf("base_url 必须以 http:// 或 https:// 开头")
		}
	}
	if src.Models == nil {
		src.Models = []string{}
	}
	return nil
}

func generateSourceID(existing []MonitorSource) string {
	used := make(map[string]bool, len(existing))
	for _, src := range existing {
		used[src.ID] = true
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("src_%d", i)
		if !used[id] {
			return id
		}
	}
}

// SortedModelStatuses 按总请求量倒序排列 (聚合页默认排序)。
func SortedModelStatuses(models []map[string]interface{}) []map[string]interface{} {
	sort.SliceStable(models, func(i, j int) bool {
		return toInt64(models[i]["total_requests"]) > toInt64(models[j]["total_requests"])
	})
	return models
}
