# 模型监控指标来源与计算逻辑

本文档详细说明前端"模型状态监控"页面展示的每个指标对应的:数据来源、SQL/内存聚合公式、口径假设、Go/Python 实现位置、以及已知的不准确点。用于评估指标准确性。

## 1. 数据源

所有指标读取上游 NewAPI 数据库的三张表:

| 表          | 用途                                                 |
| ----------- | ---------------------------------------------------- |
| `logs`      | 请求日志,几乎所有指标的来源                          |
| `abilities` | 渠道-模型-分组映射(决定哪些模型"可监控")             |
| `channels`  | 渠道元数据(`name`、`status`),用于命名和过滤启用渠道 |

`logs` 关键字段:

| 字段                | 用途                                              |
| ------------------- | ------------------------------------------------- |
| `created_at`        | Unix 秒级时间戳,用于时间窗口/slot 切分            |
| `model_name`        | 计费模型名(请求时模型,非上游实际)                 |
| `channel_id`        | 渠道维度聚合 key                                  |
| `type`              | `2`=消费成功, `5`=失败,其它(1/3/4/6)不计          |
| `use_time`          | 请求总耗时(秒)                                    |
| `prompt_tokens`     | 输入 tokens(Anthropic 语义下不含缓存读/写)        |
| `completion_tokens` | 输出 tokens                                       |
| `is_stream`         | 是否流式                                          |
| `other`             | JSON 字符串,包含 `frt`、缓存字段、路由信息等      |

`other` 中本监控关心的字段:

| `other.*`                                  | 含义                                       |
| ------------------------------------------ | ------------------------------------------ |
| `frt`                                      | 首字耗时(毫秒,仅流式)                      |
| `cache_tokens`                             | 缓存读 tokens                              |
| `cache_write_tokens`                       | 缓存写 tokens 归一化总量(新版优先字段)     |
| `cache_creation_tokens`                    | 旧版缓存写聚合字段                         |
| `cache_creation_tokens_5m`/`_1h`           | Claude 5m/1h 缓存写细分                    |
| `claude`                                   | 最终上游为 Claude Messages 协议            |
| `request_conversion`/`conversion`/...      | 格式转换链,如 `openai -> claude messages`  |
| `request_path`/`请求路径`/`path`/...       | 请求路径,兜底判断 `/v1/messages`           |

## 2. 时间窗口与 slot

| 窗口  | 时长   | slot 数 | 单 slot 长度 |
| ----- | ------ | ------- | ------------ |
| `15m` | 900s   | 15      | 60s          |
| `30m` | 1800s  | 30      | 60s          |
| `1h`  | 3600s  | 60      | 60s          |
| `6h`  | 21600s | 24      | 900s         |
| `12h` | 43200s | 24      | 1800s        |
| `24h` | 86400s | 24      | 3600s        |

slot 划分用 `FLOOR((created_at - window_start) / slot_seconds)`。

定义位置:
- Go: `backend/internal/service/model_status.go:44-51` (`timeWindowConfigs`)
- Python: `backend-py/app/model_status_service.py:26-33` (`TIME_WINDOWS`)
- 前端类型: `frontend/src/components/ModelStatusMonitor.tsx:20-52`

## 3. 整体可用性指标

### 3.1 `total_requests`

| 项目     | 内容                                                                 |
| -------- | -------------------------------------------------------------------- |
| 来源     | `COUNT(*)` 对 `logs` 中 `type IN (2, 5)` 且匹配 `model_name + window_start..now` 的行 |
| 单位     | 整数                                                                 |
| Go 位置  | `model_status.go:760-770` SQL slot 聚合内的 `total`                  |
| Python   | `model_status_service.py:970-981`                                    |
| 准确性   | 准确。若上游版本不记录 `type=5`,则 `total_requests` 实际只含成功     |

### 3.2 `success_count` / `success_rate`

| 项目     | 内容                                                                                            |
| -------- | ----------------------------------------------------------------------------------------------- |
| 公式     | `success_rate = success_count / total_requests * 100`(四舍五入到 2 位小数)                      |
| Go 口径  | `SUM(CASE WHEN type=2 AND completion_tokens > 0 THEN 1 ELSE 0 END)` — **要求有输出**            |
| Py 口径  | `SUM(CASE WHEN type=2 THEN 1 ELSE 0 END)` — **不要求输出**                                      |
| Go 位置  | `model_status.go:761`                                                                           |
| Python   | `model_status_service.py:974`                                                                   |

**Go vs Python 不一致(重要)**: Go 把 `completion_tokens=0` 的消费日志归入"空响应",从 success 里剔除;Python 把所有 `type=2` 都算 success。结果在同一数据集上,Go 的 success_rate ≤ Python 的 success_rate。建议两端口径统一。

### 3.3 `failure_count` / 失败率

| 项目     | 内容                                                                          |
| -------- | ----------------------------------------------------------------------------- |
| Go 来源  | `SUM(CASE WHEN type=5 THEN 1 ELSE 0 END)`                                     |
| Py 来源  | **未实现**,字段不存在                                                         |
| 前端展示 | `failure_count / total_requests` 二位小数,位于 `ModelStatusMonitor.tsx:2415`  |
| Go 位置  | `model_status.go:762`                                                         |
| 准确性   | Go 准确。Python 后端下,前端读到 `undefined`,失败率显示 `NaN%` / `0.00%`,需补全 |

### 3.4 `empty_count` / 空响应率

| 项目     | 内容                                                                       |
| -------- | -------------------------------------------------------------------------- |
| Go 来源  | `SUM(CASE WHEN type=2 AND completion_tokens=0 THEN 1 ELSE 0 END)`          |
| Py 来源  | **未实现**                                                                 |
| 前端展示 | `empty_count / total_requests`,`ModelStatusMonitor.tsx:2418`               |
| Go 位置  | `model_status.go:763`                                                      |
| 已知问题 | 把 "type=2 且 completion=0" 一刀切为空响应。某些合法场景(纯工具调用、模型返回 stop_reason 但无可见文本)会被误判 |

### 3.5 `current_status` 颜色

```
if total_requests == 0:        green   # 无请求等同无问题
elif success_rate >= 95:       green
elif success_rate >= 80:       yellow
else:                          red
```

实现: `model_status.go:54-64` / `model_status_service.py:90-108`。slot 级颜色也用同一函数。

注意:此处用 `success_rate`,Go 已剔除空响应、Python 未剔除,所以同一段日志在两端可能落不同色阶。

## 4. 性能指标(`performanceStats`)

性能指标走的不是 SQL slot 聚合,而是**应用层流式聚合原始日志**(避免在大表上做复杂 JSON 聚合)。批量大小 5000 行 / 批。

代码入口:
- Go: `getModelPerformanceMap` / `accumulatePerformanceRow` (`model_status.go:567-604, 415-471`)
- Python: `_get_model_performance_map` / `_accumulate_performance_row` (`model_status_service.py:702-742, 573-617`)

每条日志只统计 `type=2`(成功)。

### 4.1 `within_5s_rate` / `within_10s_rate` (首 Token 时延)

| 项目     | 内容                                                                                                 |
| -------- | ---------------------------------------------------------------------------------------------------- |
| 分母     | `timed_requests` — `first_token_time > 0` 的成功请求数                                               |
| 分子     | `first_token_time <= 5` 或 `<= 10` 的请求数                                                          |
| 时延口径 | 流式且 `other.frt > 0`: 取 `frt / 1000`(秒);否则取 `use_time` 作 fallback                            |
| 单位     | 百分比,2 位小数                                                                                      |
| 准确性   | 流式 TTFT 准确(来自上游 NewAPI)。**非流式无真实 TTFT,用总耗时代替 → 偏大,失真**                       |

### 4.2 `duration_within_10s_rate` / `duration_within_20s_rate` (总耗时)

| 项目     | 内容                                                                |
| -------- | ------------------------------------------------------------------- |
| 分母     | `duration_timed_requests` — `use_time > 0` 的成功请求                |
| 分子     | `use_time <= 10` / `<= 20` 的请求                                    |
| 含义     | 端到端请求耗时分布                                                  |

### 4.3 `completion_tps` (输出速度 tok/s)

| 项目          | 内容                                                                                            |
| ------------- | ----------------------------------------------------------------------------------------------- |
| 分子          | `Σ completion_tokens`(`type=2` 且 `completion>0` 且 `use_time>0`)                              |
| 分母          | `Σ effective_time`                                                                              |
| effective_time | 流式且 `frt>0`: `max(use_time - frt/1000, 1)`(去掉首字延迟后剩下的生成时间)<br>否则: `use_time` |
| 单位          | tok/s,2 位小数                                                                                  |
| 准确性        | 流式准确;非流式 `tok/s` 偏低(包含了排队/首字延迟,通常不可分离)                                  |

### 4.4 `cache_hit_rate` (缓存命中率) — 本次主要修订点

#### 4.4.1 分子

```
cache_tokens_sum = Σ other.cache_tokens (>0 且 type=2)
```

#### 4.4.2 分母(本次修订)

对每条 `type=2` 日志,按上游协议口径计算并累加:

| 路径                  | 触发条件                                                                              | 加到分母的值                          |
| --------------------- | ------------------------------------------------------------------------------------- | ------------------------------------- |
| Claude Messages       | `other.claude == true` 或 `request_conversion` 末段是 `claude messages` 或 `request_path` 含 `/v1/messages` | `prompt + cache_read + cache_write`   |
| OpenAI-like(默认)    | 其它                                                                                  | `MAX(prompt, cache_read)`             |

#### 4.4.3 缓存写取值(三级 fallback,与前端一致)

```
if other.cache_write_tokens > 0:
    cache_write = other.cache_write_tokens                                           # 优先,后端归一化总量
elif other.cache_creation_tokens_5m + cache_creation_tokens_1h > 0:
    cache_write = MAX(5m + 1h, cache_creation_tokens)                                # Claude 拆分写,兼容旧字段
else:
    cache_write = other.cache_creation_tokens                                        # 旧版聚合字段兜底
```

对应实现:
- 前端: `getUsageLogCacheSummary` (`web/src/helpers/log.js`,见 `usage-log-fields.md` §"前端缓存读写展示逻辑")
- Go SQL: `cacheWriteTokensExpr` (`model_status.go:217-235`)
- Go 内存: `cacheWriteTokensFromOther` (`model_status.go:486-498`)
- Python SQL: `_cache_write_tokens_expr` (`model_status_service.py:397-417`)
- Python 内存: `_cache_write_tokens_from_other`

#### 4.4.4 最终公式

```
cache_hit_rate = MIN(cache_tokens_sum, cache_denominator_sum) / cache_denominator_sum * 100
```

`MIN(...)` 是保护性:避免极少数日志间不一致导致 hit_rate > 100%。

#### 4.4.5 口径理由与已知不准确点

| 场景                    | 准确性                                                                                                            |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------- |
| Claude 原生 `/v1/messages` | ✅ 准确。Anthropic 语义下 `prompt_tokens` 是"非缓存输入",分母 `prompt + read + write` = 总输入,分子是缓存读      |
| OpenAI 转 Claude(上游) | ✅ 同上,只要 `request_conversion` 末段为 `claude messages`                                                       |
| OpenAI Chat Completions  | 🟡 近似。`prompt_tokens` 通常已经含缓存读,所以 `MAX(prompt, cache_read) ≈ prompt`,等于总输入(若上游没显式拆缓存写)。**有缓存写时偏高**(目前 OpenAI 路径没有把 cache_write 算进分母),OpenAI 此场景罕见,影响很小 |
| `input_tokens_total` 新口径 | ⚠️ `cache_hit_rate` 分母未使用。新增的 `total_input_tokens` 指标会优先读取 `other.input_tokens_total`;但命中率分母仍沿用 Claude 分支 / OpenAI-like 分支公式。当输入是文本+图片+音频混合时,cache 比例可能略低估 |
| 旧日志(无 `cache_write_tokens`) | ✅ 三级 fallback 等价于旧公式 + 拆分字段补全                                                                |

### 4.5 `cache_write_rate` (缓存写比例) — 新增独立指标

与 `cache_hit_rate` 并列展示,衡量请求中"缓存写"占总输入的比例。**仅对存在 Claude Messages 请求的模型/渠道有意义**,纯 OpenAI 渠道返回 `null`、前端展示 `N/A`。

#### 公式

```
cache_write_sum = Σ cache_write_tokens (仅 Claude 路径累加)
claude_requests = Claude Messages 请求条数

if claude_requests > 0 and cache_denominator_sum > 0:
    cache_write_rate = MIN(cache_write_sum, cache_denominator_sum) / cache_denominator_sum × 100
else:
    cache_write_rate = null   # 前端显示 "N/A"
```

#### 三种结果含义

| 情况                                            | 返回值        | 前端展示  |
| ----------------------------------------------- | ------------- | --------- |
| 时段内有 Claude 请求,且有缓存写                  | 实际百分比    | `xx.xx%`  |
| 时段内有 Claude 请求,但缓存写都是 0              | `0.00`        | `0.00%`   |
| 时段内无 Claude 请求(纯 OpenAI / 无请求)         | `null`        | `N/A`     |

这避免了 OpenAI 渠道展示 `0.00%` 造成"看起来该模型缓存写为 0"的误解。

#### 实现位置

- Go: `model_status.go:71-119` (`buildPerformanceSummary` 增加 `claudeRequests` 入参)、`:298` (`performanceStats.claudeRequests`)、`:494-499` (`accumulatePerformanceRow` Claude 分支 `stats.claudeRequests++` + `stats.cacheWriteSum += cacheWriteTokens`)
- Python: `model_status_service.py:65-66, 86` (dataclass)、`:200-218` (`_build_performance_summary` 增加 `claude_requests` 参数)、`:472` (`_empty_performance_stats`)、`:629-634` (`_accumulate_performance_row` Claude 分支累计)
- 前端: `ModelStatusMonitor.tsx` 接口字段 + `formatCacheWriteRate`(null → `"N/A"`) + `MetricPill label="缓存" detail="写比例"`,7 列 grid

#### 准确性说明

| 场景                    | 表现                                                                                                              |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------- |
| Claude Messages         | ✅ 准确。等于"本时段被写入缓存的 tokens 占总输入 tokens 的比例"                                                  |
| OpenAI-like             | 显示 `N/A`(后端返回 `null`,前端 fallback)。OpenAI 渠道极少有缓存写,符合预期                                       |
| 混合渠道(同模型既走 Claude 又走 OpenAI) | 返回数值。分子只算 Claude 段缓存写,分母却包含两段总输入 → 偏低。实际很罕见,但需注意                  |
| 命中率 + 写比例 之和    | 在纯 Claude 场景下,反映"已缓存 + 本次新写入缓存"占总输入的比例;`100% - hit_rate - write_rate ≈ 真实非缓存输入占比` |

### 4.6 Token 计数指标

模型卡片和渠道摘要会额外展示 4 个 token 总量字段。它们与 `cache_hit_rate` / `cache_write_rate` 同源,都只统计 `type=2` 的成功日志。

| 字段 | 含义 | 计算口径 |
| ---- | ---- | -------- |
| `cache_hit_tokens` | 缓存命中/缓存读 tokens | `Σ other.cache_tokens` |
| `cache_write_tokens` | 缓存写 tokens | `Σ cache_write_tokens_from_other(other)`。不限制 Claude 路径,用于展示真实缓存写总量 |
| `total_input_tokens` | 总输入 tokens | 优先 `other.input_tokens_total`;否则 Claude 路径为 `prompt + cache_read + cache_write`,OpenAI-like 路径为 `MAX(prompt, cache_read)` |
| `total_output_tokens` | 总输出 tokens | `Σ completion_tokens` |

历史 SQLite 快照同步保存 `cache_write_tokens_sum`、`input_tokens_sum`、`output_tokens_sum`。已有历史库会自动补列,但旧日期已聚合数据的新 token 字段默认为 0;需要重新聚合对应日期后才会有完整 token 总量。

## 5. 状态分布(slot_data)

`slot_data` 是按时间桶展开的 `total/success/failure/empty/success_rate/status` 数组,渲染为时间条。

- Go: `model_status.go:758-841` 单 SQL 聚合 + 内存填充
- Python: `model_status_service.py:970-1038`

每个 slot 的 success_rate / status 用 §3.5 同样的阈值。

## 6. 渠道维度指标(`ChannelPerformanceSummary`)

接口: `GET /channels/performance?window=...`

字段与模型维度一致(无 `failure_count`/`empty_count`/`slot_data`),只是按 `channel_id` 分组。仅保留 `success_requests > 0` 的渠道,按请求数倒序。

| 字段 | 计算 | 与模型维度差异 |
| --- | --- | --- |
| `channel_id`/`channel_name` | `JOIN channels` 取 `name`,缺名时用 `Channel#<id>` | — |
| `total_requests` | `accumulatePerformanceRow` 中 `success_requests`,即 `type=2` 总数 | **不等于** §3.1 的 `total_requests`(模型维度 `type IN (2,5)`)。**口径不一致** |
| 其它性能字段 | 同 §4 | — |

⚠️ **口径不一致**: 模型卡片显示的 `total_requests` 含失败,渠道卡片显示的 `total_requests` 只含成功。两个数字直接对比会困惑用户。

- Go: `model_status.go:606-699`
- Python: `model_status_service.py:744-819`

## 7. "可监控模型"列表

接口: `GET /models` → `GetAvailableModels`

逻辑:
- 来源 `abilities a INNER JOIN channels c ON c.id = a.channel_id WHERE c.status = 1`
- 加上 24h 内的 `request_count_24h`(用 `logs` 表 `type IN (2,5)`)
- 排序: 24h 请求数倒序

代码:
- Go: `GetAvailableModels` (`model_status.go:702-726`),24h 计数在 SQL 内一并算出
- Python: `get_available_models_with_stats` (`model_status_service.py:866-931`),分两步查

## 8. 缓存层(查询缓存,不是 cache_hit_rate)

为减少 DB 压力,所有读接口都做了短期缓存。**这里的 cache 是查询结果缓存,与 §4.4 模型的"缓存命中率"无关**,容易混淆。

| key 前缀                            | TTL    | 作用                                |
| ----------------------------------- | ------ | ----------------------------------- |
| `model_status:<model>:<window>`     | 30s    | 单模型状态                          |
| `model_status:channel_performance:<window>` | 30s | 渠道性能                          |
| `model_status:available_models`     | 5min   | 模型列表                            |
| `model_status:token_groups`         | 5min   | 分组列表                            |
| 前端 `no_cache=true` 参数           | -      | 手动刷新时绕过缓存                  |

Go 用内存 cache(`internal/cache`),Python 用 SQLite 表 `model_status_cache`。

## 9. 已知不一致 / 待评估

| 问题                                          | 影响                                              | 建议                                                            |
| --------------------------------------------- | ------------------------------------------------- | --------------------------------------------------------------- |
| Go/Python success 口径不一致(§3.2)            | 同数据两端口径不同色                              | Python 也加 `AND completion_tokens > 0`,或两端都改成宽口径      |
| Python 无 `failure_count`/`empty_count`(§3.3) | Python 后端下前端失败率/空响应率为 `NaN`/`0`      | Python `get_model_status` 同步加这两列                          |
| 非流式 TTFT 用 `use_time` 代替(§4.1)          | 非流式模型 `within_5s/10s_rate` 偏高              | 文档显式标注;或非流式不参与统计                                 |
| OpenAI 分支 cache_write 未计入分母(§4.4.5)    | OpenAI Chat Completions 出现缓存写时命中率偏高    | OpenAI 场景缓存写极少;若上游开始普遍提供,需同步增加              |
| `input_tokens_total` 未用于 `cache_hit_rate` 分母(§4.4.5) | 含图/音多模态时命中率分母可能略低估;`total_input_tokens` 不受此限制 | 若该字段普及,可评估是否也用于 OpenAI-like 命中率分母 |
| `empty_count` 误判工具调用(§3.4)              | 纯 tool_use 响应被算空响应                        | 若 `other` 里能识别 tool_use,从 empty 中剔除                    |
| 渠道维度 `total_requests` 不含失败(§6)        | 与模型维度数字不可直接对比                        | 渠道维度同样做 `type IN (2,5)` 聚合,或文案区分                  |

## 10. 数据流总结

```
NewAPI logs 表
   │
   ├─[1] SQL slot 聚合(单查询,只算 total/success/failure/empty)
   │       └─ 用 type IN (2,5) 过滤 → slot_data + 顶层 4 个计数
   │
   └─[2] 应用层流式扫描(批 5000 行)
           └─ 仅 type=2 → 计算 TTFT/总耗时/tok-s/cache_hit/token 总量
               └─ 用 other JSON + 路径/转换链识别 Claude
                   └─ Claude:  分母 += prompt + read + write
                       非 Claude:分母 += MAX(prompt, read)
                   └─ 分子 += cache_tokens
                   └─ token 总量 += 缓存读/缓存写/总输入/总输出

最后 cache_hit_rate = MIN(分子, 分母) / 分母 * 100
```

## 11. 验证建议

对照 NewAPI 前端"使用日志"页面,挑一个 Claude 模型:

1. 取最近 1h 所有日志,前端逐行汇总:
   - `prompt_tokens` 累加 = A
   - `other.cache_tokens` 累加 = B(缓存读)
   - `getUsageLogCacheSummary().cacheWriteTokens` 累加 = C(缓存写)
2. 期望: 监控页 `cache_hit_rate ≈ B / (A + B + C) * 100`
3. 监控页接口 `GET /status/batch?window=1h&no_cache=true` 取该模型的 `cache_hit_rate`,误差应在 ±0.5%(四舍五入)

OpenAI Chat Completions 模型同理,把分母换成 `MAX(prompt, cache_tokens)` 累加。
