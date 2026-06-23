# 多源模型监控 (Multi-Source Model Monitor)

本文档说明「多源模型监控」的设计、口径、配置项、接口与部署方式。它在原有单源「模型状态监控」之上扩展，**不改动**原有监控与 `embed.html`，两套并行存在。

相关源码：

| 层 | 文件 |
| --- | --- |
| 错误分类规则 | `backend/internal/service/error_classify.go` |
| 带分类的模型状态 | `backend/internal/service/model_status_classified.go` |
| 多源聚合服务 | `backend/internal/service/multi_source.go` |
| HTTP 接口与路由 | `backend/internal/handler/multi_source.go` |
| 路由注册 | `backend/cmd/server/main.go` |
| 后台管理页 | `frontend/src/components/MultiSourceMonitorAdmin.tsx` |
| 公开聚合页 | `frontend/src/components/MultiSourceEmbed.tsx` + `frontend/multi.html` + `frontend/src/multi.tsx` |
| 单测 | `backend/internal/service/error_classify_test.go` |

---

## 1. 目标

1. **复刻一个公开的模型监控页**，无需登录即可查看（`/multi.html`）。
2. **聚合多个信息源**：一个是本机服务（直连本地 NewAPI 数据库），其余是同款工具的其它部署（通过它们的公开接口拉取）。
3. 后台可配置「**展示某个源下的某些模型**」。
4. 区分错误类别：**模型侧报错 / 用户请求格式错误 / 限速**，并据此计算成功率。
5. 公开页**仅展示最近 xx 分钟**数据，**每 xx 秒刷新一次**；窗口与刷新间隔后台可配，**热更新**。

---

## 2. 架构：HTTP 联邦

```
                 ┌──────────────────────────────────────────┐
                 │      聚合端 (本次部署, 提供公开页)         │
                 │                                            │
   公开页  ──────┤  GET /api/embed/multi-source/aggregated    │
 /multi.html     │            │                               │
                 │            ├─ 本机源 ─► 直连本地 DB         │
                 │            │           GetClassifiedModels  │
                 │            │                               │
                 │            ├─ 远程源A ─HTTP─► peerA         │
                 │            └─ 远程源B ─HTTP─► peerB         │
                 └──────────────────────────────────────────┘
                                    │ POST
                                    ▼
        peer: POST /api/embed/model-status/classified/batch?window=xx
              （对端用自己的本地 DB 算好分类数据返回）
```

- **本机源**：聚合端直接调用本进程的 `ModelStatusService.GetClassifiedModelsStatus`，无 HTTP 开销。
- **远程源**：聚合端通过 HTTP 调用对端的「leaf」接口，拿对端**已经算好并分类好**的模型状态，打上源标签后合并。**不需要对端的数据库凭据**。
- 浏览器只与聚合端通信，远程源的地址/令牌不暴露给前端，也规避了跨域问题。

> 远程源必须是**同款工具且已升级到包含 `classified/batch` 接口的版本**，否则聚合端拉不到分类数据（该源会显示拉取失败）。

---

## 3. 错误分类口径

### 3.1 三个类别

| 类别 | 常量 | 含义 | 是否计入成功率分母 |
| --- | --- | --- | --- |
| 模型侧错误 | `model_side` | 上游/模型返回的错误（5xx、网关错误、上游 overloaded 等）；**限速默认也归此类** | ✅ 计入 |
| 用户格式错误 | `user_format` | 用户请求本身的问题（4xx 非 429，如 400 / 422 / `invalid_request_error`） | ❌ 不计入 |
| 限速 | `rate_limit` | 429 / 速率限制 | ✅ 计入（见 3.4） |

### 3.2 判定来源

只对 `logs` 表中 `type=5`（失败）的行做分类，读取其 `other` JSON：

| `other.*` | 用途 |
| --- | --- |
| `status_code`（优先）/ `upstream_error_status` / `upstream_status_code` / `code` | HTTP 状态码（兼容字符串与数字两种存储） |
| `error_type` | 错误类型文本 |
| `upstream_error_type` | 上游错误类型文本 |
| `error_code` / `upstream_error_code` | 错误码文本 |

### 3.3 判定优先级（`ErrorRuleConfig.classify`）

```
1. 状态码精确命中：
     status_code ∈ rate_limit_status_codes      → rate_limit   (默认 [429])
     status_code ∈ user_format_status_codes     → user_format  (默认 [400,401,403,404,413,422])
2. 状态码区间：
     user_format_min ≤ status_code ≤ user_format_max → user_format (默认 400..499)
     status_code ≥ 500                                → model_side
3. 关键字兜底（状态码缺失或未命中时，对 error_type/upstream_error_type/error_code 做不区分大小写子串匹配）：
     命中 rate_limit_keywords   → rate_limit
     命中 user_format_keywords  → user_format
4. 兜底：未知失败一律 model_side（保守，不让未知错误抬高成功率）
```

> 默认规则见 `DefaultErrorRules()`。所有阈值/列表/关键字**后台可编辑、热更新**（见 §5）。

### 3.4 成功率公式（核心口径）

> 需求：「**根据模型侧报错来显示成功率**」「**限速要计算为模型侧错误，平台本身不做限速**」。

```
model_success_rate = success / 分母 × 100

分母 = total − user_format               (count_rate_limit_as_model_error = true，默认)
分母 = total − user_format − rate_limit   (count_rate_limit_as_model_error = false)

success = type=2 且 completion_tokens > 0  (沿用原监控的严格成功定义)
total   = 该窗口内 type∈(2,5) 的请求总数
```

含义：**用户格式错误是用户自己的问题，不该拉低模型可用性**，所以始终从分母剔除；**限速默认视为模型侧资源紧张**（平台本身不限速），计入分母。

边界：
- 分母 ≤ 0（例如全是格式错误、或无请求）→ 成功率视为 **100%**（模型无责任）。
- 实现：`modelSuccessRate()`（`model_status_classified.go`），单测见 `error_classify_test.go::TestModelSuccessRate`。

### 3.5 状态颜色

公开页模型卡片/时间条的颜色用 `model_success_rate` 配合原有阈值函数 `getStatusColor`：

```
total(剔除格式错误后) == 0  → green   (无请求 = 无问题)
model_success_rate ≥ 95     → green
model_success_rate ≥ 80     → yellow
否则                        → red
```

---

## 4. 返回字段

`classified/batch` 与 `aggregated` 返回的每个模型对象，在原 `ModelStatus` 字段基础上**新增**：

| 字段 | 含义 |
| --- | --- |
| `model_error_count` | 模型侧错误数 |
| `format_error_count` | 用户格式错误数 |
| `rate_limit_count` | 限速数 |
| `model_success_rate` | 模型侧成功率（§3.4） |
| `model_current_status` | 基于 `model_success_rate` 的颜色 |
| `source_id` / `source_name` | 该模型所属源（仅聚合接口附加） |

`slot_data` 每个 slot 同步新增 `model_error_count` / `format_error_count` / `rate_limit_count` / `model_success_rate` / `model_status`。

> 原有字段（`success_rate`、`failure_count`、`empty_count`、`slot.status` 等）保持不变，旧前端/旧监控不受影响。

---

## 5. 配置与热更新

所有配置存 Redis（`TTL=0` 永不过期），**每请求读取**，因此修改后**立即生效**（同实例本地缓存最多 30s 延迟，见原 `model-monitor-metrics.md` §8）。保存配置时会清空聚合结果缓存。

| 配置 | Redis key | 默认值 |
| --- | --- | --- |
| 源列表 | `multi_source:sources` | 仅含一个本机源 `local` |
| 展示配置 | `multi_source:config` | 见下 |
| 错误规则 | `multi_source:error_rules` | `DefaultErrorRules()` |
| 聚合结果缓存 | `multi_source:agg:<window>` | TTL = `agg_cache_ttl`（默认 `refresh/2`，最小 5s） |

展示配置 `MultiSourceConfig`：

| 字段 | 含义 | 默认 |
| --- | --- | --- |
| `time_window` | **仅展示最近 xxmin**，支持 `15m/30m/1h/6h/12h/24h` 及自定义如 `45min`/`2h` | `15m` |
| `refresh_interval` | **每 xx 秒刷新一次**（前端轮询） | `60` |
| `title` | 公开页标题 | `模型监控` |
| `theme` | 主题（复用现有主题集，浅色/深色自动适配） | `daylight` |
| `agg_cache_ttl` | 聚合结果服务端缓存秒数 | `refresh/2` |

源 `MonitorSource`：

| 字段 | 含义 |
| --- | --- |
| `id` | 源 ID（本机固定 `local`；远程自动生成 `src_N`） |
| `name` | 显示名 |
| `type` | `local` / `remote_http` |
| `base_url` | 远程源根地址，如 `https://peer.example.com`（远程必填） |
| `token` | 远程源访问令牌（可选，作 `Authorization: Bearer`） |
| `enabled` | 是否参与聚合 |
| `models` | 该源要展示的模型名列表 |

> 本机源 `local` 不可删除，只能停用。

---

## 6. 接口

### 6.1 公开端（无鉴权）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/embed/multi-source/config` | 公开页配置（标题/主题/窗口/刷新 + 可选项） |
| GET | `/api/embed/multi-source/aggregated` | 聚合后的分类数据（带服务端缓存；`?no_cache=true` 绕过） |
| POST | `/api/embed/model-status/classified/batch?window=xx` | **leaf**：返回本机已分类的模型状态，body 为模型名数组。供其它部署联邦拉取 |

> 以上路径同时提供 `/api/model-status/embed/...` 与 `/api/multi-source/embed/...` 兼容别名。

### 6.2 管理端（需鉴权）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/multi-source/sources` | 列出所有源 |
| POST | `/api/multi-source/sources` | 新增源 |
| PUT | `/api/multi-source/sources/:id` | 更新源 |
| DELETE | `/api/multi-source/sources/:id` | 删除源（本机源除外） |
| PUT | `/api/multi-source/sources/:id/models` | 设置某源展示的模型 |
| GET | `/api/multi-source/sources/:id/available-models` | 该源可选模型（本机查本地；远程代理对端 `/models`） |
| GET / PUT | `/api/multi-source/config` | 读写展示配置 |
| GET / PUT | `/api/multi-source/error-rules` | 读写错误分类规则 |
| GET | `/api/multi-source/aggregated` | 管理端预览（强制不走缓存） |

---

## 7. 前端

| 页面 | 入口 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| 后台管理 | 主应用 `多源监控` tab | 是 | 源增删改、每源模型多选、窗口/刷新/标题/主题、错误规则编辑、一键打开公开页 |
| 公开聚合页 | `/multi.html` | 否 | 按源分组展示，每模型显示模型侧成功率 + 三类错误拆分 + 时间条热力图，按 `refresh_interval` 自动刷新 |

公开页 URL 参数：`?theme=<id>` 覆盖主题，`?refresh=<秒>` 覆盖刷新间隔（便于 iframe 嵌入）。

---

## 8. 部署注意

1. **构建产物**：`multi.html` 已加入 `vite.config.ts` 多入口，`npm run build` 自动产出；Dockerfile 把整个 `dist/` 拷到 Nginx，`location /` 的 `try_files` 直接命中 `/multi.html`，无需额外路由。
2. **iframe 嵌入**：Nginx 默认 `X-Frame-Options: SAMEORIGIN`，与现有 `embed.html` 一致——仅同源可嵌；需跨域嵌入时自行调整该响应头。
3. **远程源版本**：远程源须为同款工具且含 `classified/batch` 接口，否则该源在公开页显示「数据拉取失败」。
4. **数据字段依赖**：错误分类依赖 `logs.other.status_code` 等字段。上游 NewAPI 版本若不写这些字段，会退化到关键字兜底，最终未知错误**保守归为模型侧**（可能略微拉低成功率）。
5. **缓存**：聚合结果有服务端短 TTL 缓存保护远程源；改配置即清缓存，公开页下次刷新即生效。

---

## 9. 与单源监控的关系

| | 单源监控（原有） | 多源监控（本功能） |
| --- | --- | --- |
| 公开页 | `/embed.html` | `/multi.html` |
| 数据源 | 仅本地 DB | 本地 + 多个远程 |
| 成功率口径 | `success / total` | `success / (total − 格式错误)` |
| 错误分类 | 仅 success/failure/empty | model_side / user_format / rate_limit |
| 配置命名空间 | `model_status:*` | `multi_source:*` |
| 代码 | `model_status.go` | 新增文件，复用但不改动 `model_status.go` |

两者**完全独立、互不影响**，可同时启用。
