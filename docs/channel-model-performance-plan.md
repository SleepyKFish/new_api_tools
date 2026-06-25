# 渠道下模型性能明细方案

## 目标

在“模型状态监控”的渠道监控区域中，点击某个渠道后，可以查看该渠道下面每个模型在同一时间段内的性能情况。

必须同时支持：

- 实时滑动窗口：`15m / 30m / 1h / 6h / 12h / 24h / 自定义窗口`
- 历史日期：按已有历史快照日期查看

范围确认：

- 实时渠道下钻和历史渠道下钻都属于本期完整功能。
- 历史渠道点击后必须能查看该渠道下的模型性能明细。
- 历史明细不能作为后续二期，也不能在点击时临时扫描主 `logs` 表。

核心约束：

- 点击渠道详情时，不再扫描主 `logs` 表。
- 实时渠道卡片与渠道下模型明细来自**同一次扫描**：搭 `GetChannelPerformanceSummaries`（渠道卡片本来就有的那次扫描）的便车，同一轮循环里多累计一层 `channel + model`。
- 历史渠道卡片与渠道下模型明细必须来自同一次历史快照。
- 指标口径复用现有模型/渠道性能算法，不另起一套统计逻辑。
- 不引入 `snapshot_id`：明细与渠道卡片写入同一缓存、同 TTL，缓存键用 `window + channel_id`，两者同生共死，天然同源。

## 总体设计

日志扫描时同时维护三类聚合：

```go
modelStats[modelName]
channelStats[channelID]
channelModelStats[channelID][modelName]
```

其中 `channelModelStats[channelID][modelName]` 使用和模型卡片一致的累计结构：

```go
channelModelAvailability[channelID][modelName] *availabilityStats
channelModelPerformance[channelID][modelName]  *performanceStats
```

每条日志只处理一次：

```go
channelID := toInt64(row["channel_id"])
modelName := toString(row["model_name"])

channelAvailability[channelID].accumulate(row, ...)
accumulatePerformanceRow(channelPerformance[channelID], row)

channelModelAvailability[channelID][modelName].accumulate(row, ...)
accumulatePerformanceRow(channelModelPerformance[channelID][modelName], row)
```

历史快照同理，只是累计结构换成 `dailyPerfStats` 和 `slotCounts`：

```go
dailyChannelStats[channelID]
dailyChannelModelStats[channelID][modelName]
```

## 实时模式

### 聚合入口

复用渠道卡片现有扫描路径：

- `ModelStatusService.GetChannelPerformanceSummaries`

该方法已经独立扫描当前窗口的 `logs`，并按 `channel_id` 聚合渠道卡片，结果写入缓存
`model_status:channel_performance:v2:<window>` 后返回。

渠道下模型明细就挂在这一次扫描上：同一轮批扫循环里，除了按 `channelID` 累计，再多累计一层
`channelID + modelName`。这样渠道卡片与渠道下模型明细天然来自**同一次扫描**，无需 `snapshot_id`。

扫描循环新增累计结构：

```go
channelModelAvailability map[int64]map[string]*availabilityStats
channelModelPerformance  map[int64]map[string]*performanceStats
```

每条日志只处理一次：

```go
channelID := toInt64(row["channel_id"])
modelName := toString(row["model_name"])

// 现有渠道维度
availabilityByChannel[channelID].accumulate(row, ...)
accumulatePerformanceRow(statsByChannel[channelID], row)

// 新增渠道 + 模型维度
channelModelAvailability[channelID][modelName].accumulate(row, ...)
accumulatePerformanceRow(channelModelPerformance[channelID][modelName], row)
```

### 缓存策略

渠道卡片接口不直接返回所有模型明细，避免响应体膨胀。

`GetChannelPerformanceSummaries` 扫描完成后，把每个渠道的模型明细写入缓存，**键按 `window + channel_id`**，
TTL 与渠道卡片缓存一致：

```text
model_status:channel_model_detail:v2:<window>:<channel_id>
```

要点：

- 不引入 `snapshot_id`。明细缓存与渠道卡片缓存在同一次扫描里一起写入，TTL 相同。
- 渠道卡片缓存过期时，下一次请求重新扫描，明细缓存随之刷新，两者永远是同一批数据。
- 缓存内容只保存有请求的 `channel_id + model_name` 组合，不为无请求模型补零。

点击渠道时：

```http
GET /api/model-status/channels/{channel_id}/models/performance?window=24h&limit=100&offset=0
```

处理逻辑：

1. 按 `window + channel_id` 只读缓存。
2. 不访问主数据库。
3. 缓存命中后按请求数倒序分页返回该渠道下的模型卡片数据。
4. 缓存未命中（罕见，仅在卡片缓存刚好过期且尚未重建的窗口）时，调用一次
   `GetChannelPerformanceSummaries(window)` 触发重建后再读，仍不单独扫该渠道的 `logs`。

### 实时接口

新增认证接口：

```http
GET /api/model-status/channels/:channel_id/models/performance
```

查询参数：

```text
window       必填。与渠道卡片当前窗口一致，如 24h / 1h / 自定义窗口。
limit        默认 100，最大 500。
offset       默认 0。
```

响应：

```json
{
  "success": true,
  "channel_id": 8,
  "channel_name": "anthropic",
  "window": "24h",
  "total": 12,
  "limit": 100,
  "offset": 0,
  "has_more": false,
  "data": [
    {
      "model_name": "claude-sonnet-4",
      "display_name": "claude-sonnet-4",
      "time_window": "24h",
      "total_requests": 300,
      "success_count": 295,
      "failure_count": 3,
      "empty_count": 2,
      "model_availability_rate": 98.33,
      "within_5s_rate": 92.1,
      "completion_tps": 48.2,
      "slot_data": []
    }
  ]
}
```

其中 `has_more = offset + len(data) < total`，`total` 为该渠道下有请求的模型组合数。

可选公开嵌入接口：

```http
GET /api/model-status/embed/channels/:channel_id/models/performance
GET /api/embed/model-status/channels/:channel_id/models/performance
```

嵌入页面的渠道下钻使用同样的 `window + channel_id` 缓存键机制。

## 历史模式

历史不能依赖实时短缓存，也不能点击时扫主 `logs` 表。历史明细必须在生成每日快照时写入历史库。

历史渠道下钻是必做能力：用户选择某个历史日期后，点击该日期的渠道卡片，必须返回该渠道在当天 24 小时快照中的模型明细。

### 历史库改动

当前历史 SQLite 已有：

- `model_daily_summary`
- `model_hourly_slot`
- `model_daily_channel`
- `channel_hourly_slot`

新增两张表：

```sql
CREATE TABLE IF NOT EXISTS model_daily_channel_model (
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
  use_time_sum REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (date, channel_id, model_name)
);

CREATE TABLE IF NOT EXISTS channel_model_hourly_slot (
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
  use_time_sum REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (date, channel_id, model_name, slot_idx)
);
```

索引：

```sql
CREATE INDEX IF NOT EXISTS idx_daily_channel_model_date_channel
ON model_daily_channel_model(date, channel_id, total_requests DESC);

CREATE INDEX IF NOT EXISTS idx_channel_model_hourly_date_channel_model
ON channel_model_hourly_slot(date, channel_id, model_name);
```

### 历史快照结构

扩展 `daySnapshot`：

```go
type daySnapshot struct {
    date       string
    startTS    int64
    models     map[string]*dailyPerfStats
    slots      map[string]map[int]*slotCounts
    channels   map[int64]*dailyPerfStats
    chanSlot   map[int64]map[int]*slotCounts
    chanName   map[int64]string

    channelModels map[int64]map[string]*dailyPerfStats
    chanModelSlot map[int64]map[string]map[int]*slotCounts
}
```

扩展 `accumulateDayRow`：

```go
if modelName != "" {
    channelModels := snap.channelModels[channelID]
    if channelModels == nil {
        channelModels = make(map[string]*dailyPerfStats)
        snap.channelModels[channelID] = channelModels
    }
    st := channelModels[modelName]
    if st == nil {
        st = &dailyPerfStats{}
        channelModels[modelName] = st
    }

    // availability counters
    // performance counters
    // hourly slot counters
}
```

扩展 `SaveDay`：

- 删除旧日期数据时加入两张新表。
- 插入 `model_daily_channel_model`。
- 插入 `channel_model_hourly_slot`。
- `SaveDay` 仍保持幂等：同一天重新聚合会替换所有历史快照行。

### 历史接口

新增：

```http
GET /api/model-status/history/channels/:channel_id/models/performance
```

查询参数：

```text
date    必填，YYYY-MM-DD
limit   默认 100，最大 500
offset  默认 0
```

响应与实时明细保持同形：

```json
{
  "success": true,
  "date": "2026-06-24",
  "time_window": "24h",
  "channel_id": 8,
  "channel_name": "anthropic",
  "total": 12,
  "limit": 100,
  "offset": 0,
  "has_more": false,
  "data": []
}
```

历史明细构建逻辑复用：

- `perfSummaryFromDaily`
- `buildAvailabilitySlotData`
- `modelSuccessRate`
- `getStatusColor`

### 旧历史数据处理

已有历史日期没有 `channel + model` 明细。完整上线需要提供回填策略：

1. Schema 升级只创建新表，不自动扫描主库。
2. 新增后台回填能力：按日期调用现有 `AggregateDay(date)` 重新生成快照。
3. 可先回填最近 N 天，再按需回填更早日期。
4. 历史详情接口遇到旧日期无明细时，返回：

```json
{
  "success": false,
  "code": "DETAIL_NOT_BUILT",
  "message": "该日期尚未生成渠道模型明细，请先回填历史快照"
}
```

不允许在历史详情点击时临时扫描主 `logs` 表。

回填入口（已实现）：

```http
POST /api/model-status/history/backfill?date=YYYY-MM-DD
```

该接口对指定日期同步执行 `AggregateDay(date)`，重新扫描主 `logs` 表并替换当天快照（含 `channel + model` 明细）。仅认证接口，不暴露在 embed 路由。回填完成后该日期的渠道下钻即可正常返回明细。

## 前端交互

### 实时模式

1. 刷新模型监控时调用现有渠道性能汇总接口。
2. 记录当前 `window`。
3. 渠道卡片显示 `model_count`（可选，由渠道卡片响应附带）。
4. 点击渠道卡片时调用：

```text
/api/model-status/channels/{channel_id}/models/performance?window=24h
```

5. 展开该渠道的模型性能列表。
6. 模型明细卡片复用现有 `ModelStatusCard` 展示。

### 历史模式

1. 用户选择历史日期后，渠道卡片来自 `history/channels/performance?date=...`。
2. 点击渠道卡片时调用：

```text
/api/model-status/history/channels/{channel_id}/models/performance?date=...
```

3. 展开历史渠道下模型性能列表。

### UI 状态

需要处理：

- 加载中
- 空数据
- 历史日期未回填明细
- 分页或“加载更多”
- 收起/切换渠道

实时模式不需要“缓存过期提示刷新”：缓存未命中时后端自动重建，前端无感知。

## 性能与内存边界

### 数据库压力

实时：

- 不新增点击时数据库查询。
- 日志扫描次数不变。
- 只是同一轮扫描里多维护 `channelID + modelName` 聚合。

历史：

- 点击历史详情只读 SQLite 历史快照。
- 只有生成快照或回填快照时扫描主 `logs` 表。

### 服务内存压力

额外内存主要与实际出现过的组合数相关：

```text
P = count(distinct channel_id, model_name)
```

复杂度：

```text
实时扫描：O(N)
渠道汇总：O(C)
渠道模型明细：O(P * slot_count)
```

其中：

- `N` 是时间窗口内日志条数
- `C` 是有请求的渠道数
- `P` 是实际出现过的渠道-模型组合数
- 实时自定义窗口通常最多 24 个 slot，1h 内是 60 个 slot
- 历史固定 24 个 hourly slot

控制策略：

- 渠道卡片不直接嵌套所有模型明细。
- 明细按点击渠道返回。
- 明细接口分页，默认 `limit=100`，最大 `500`。
- 明细缓存与渠道卡片缓存同键族、同 TTL，不单独维护短缓存生命周期。
- 只统计有请求的模型组合，不为无请求模型补零。

## 指标口径

渠道下模型明细与现有模型卡片一致：

- `total_requests`：`type IN (2, 5)` 与空响应都纳入可用性统计
- `success_count`：`type=2 AND completion_tokens > 0`
- `failure_count`：`type=5`
- `empty_count`：`type=2 AND completion_tokens = 0`
- `format_error_count`、`rate_limit_count`：复用错误分类规则
- `model_availability_rate`：复用 `modelSuccessRate`
- 首 Token、总耗时、缓存、TPS：复用 `accumulatePerformanceRow` / `accumulateDailyPerformance`
- `slot_data`：复用 `buildAvailabilitySlotData`

## 实施步骤

1. 清理前置方案中“点击后单独查渠道日志”“snapshot_id 短缓存”的草稿，避免误用。
2. 在 `GetChannelPerformanceSummaries` 扫描循环中增加 `channelModelAvailability` 和 `channelModelPerformance` 累计。
3. 扫描完成后构建每个渠道的模型明细列表（按请求数倒序），写入缓存键 `model_status:channel_model_detail:v2:<window>:<channel_id>`，TTL 与渠道卡片一致。
4. 渠道卡片响应可附带 `model_count`。
5. 新增实时渠道模型明细接口，按 `window + channel_id` 只读缓存；未命中时触发一次 `GetChannelPerformanceSummaries` 重建后再读。
6. 扩展历史 SQLite schema，增加渠道模型日汇总和小时槽表。
7. 扩展 `daySnapshot`、`accumulateDayRow`、`SaveDay`。
8. 新增历史渠道模型明细查询方法和 HTTP 接口（直接读 SQLite）。
9. 前端渠道卡片支持点击展开，实时和历史分别调用对应接口。
10. 补测试：实时聚合、缓存命中/重建、历史保存/读取、旧历史无明细、分页。
11. 提供回填入口或运维说明，对旧历史日期重新执行 `AggregateDay(date)`。
12. 历史渠道下钻通过验收后，功能才算完整完成。

## 验收标准

- 实时渠道详情点击不触发主 `logs` 表查询。
- 实时详情与当前渠道卡片来自同一次扫描、时间窗口一致。
- 历史详情点击不触发主 `logs` 表查询。
- 历史详情来自 SQLite 快照，并与历史渠道卡片同一天。
- 渠道下模型卡片指标与普通模型卡片口径一致。
- 大量渠道和模型组合下，渠道列表响应体不会因模型明细显著膨胀。
- 实时缓存未命中时自动重建、前端无感知；旧历史未回填时有明确前端提示。
