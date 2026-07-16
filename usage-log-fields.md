# new-api 使用日志接口与缓存字段说明

本文档说明使用日志接口返回结构、`other` 字段内容，以及前端如何展示缓存读和缓存写。

## 接口入口

使用日志页面主要调用以下接口：

| 接口                     | 权限         | 用途                 |
| ------------------------ | ------------ | -------------------- |
| `GET /api/log/`          | 管理员       | 查询全部用户使用日志 |
| `GET /api/log/self`      | 登录用户     | 查询当前用户使用日志 |
| `GET /api/log/stat`      | 管理员       | 查询日志统计         |
| `GET /api/log/self/stat` | 登录用户     | 查询当前用户日志统计 |
| `GET /api/log/token`     | 只读令牌鉴权 | 查询当前令牌最近日志 |

列表接口支持的查询参数：

| 参数              | 说明                                                                               |
| ----------------- | ---------------------------------------------------------------------------------- |
| `p`               | 页码，从 1 开始                                                                    |
| `page_size`       | 每页数量，最大 100                                                                 |
| `type`            | 日志类型。`0` 表示全部，`1` 充值，`2` 消费，`3` 管理，`4` 系统，`5` 错误，`6` 退款 |
| `start_timestamp` | 开始时间戳，秒                                                                     |
| `end_timestamp`   | 结束时间戳，秒                                                                     |
| `token_name`      | 令牌名称                                                                           |
| `model_name`      | 模型名称，支持 LIKE 查询                                                           |
| `group`           | 分组                                                                               |
| `request_id`      | 请求 ID                                                                            |
| `username`        | 用户名，仅管理员接口                                                               |
| `channel`         | 渠道 ID，仅管理员接口                                                              |

统一响应结构：

```json
{
  "success": true,
  "message": "",
  "data": {}
}
```

列表接口的 `data` 为分页对象：

```json
{
  "page": 1,
  "page_size": 20,
  "total": 100,
  "items": []
}
```

## 日志行字段

`data.items[]` 每一项来自后端 `model.Log`。

| JSON 字段           | 类型    | 说明                                                                     |
| ------------------- | ------- | ------------------------------------------------------------------------ |
| `id`                | number  | 日志 ID。管理员接口返回数据库 ID；用户自查接口会被重写为当前查询结果序号 |
| `user_id`           | number  | 用户 ID                                                                  |
| `created_at`        | number  | 创建时间戳，秒                                                           |
| `type`              | number  | 日志类型                                                                 |
| `content`           | string  | 日志内容或额外说明                                                       |
| `username`          | string  | 用户名                                                                   |
| `token_name`        | string  | 令牌名称                                                                 |
| `model_name`        | string  | 请求并计费模型名称                                                       |
| `quota`             | number  | 本次消费、退款或变更额度                                                 |
| `prompt_tokens`     | number  | 输入 tokens。Anthropic 语义下通常只含非缓存输入                          |
| `completion_tokens` | number  | 输出 tokens                                                              |
| `use_time`          | number  | 请求耗时，秒                                                             |
| `is_stream`         | boolean | 是否流式请求                                                             |
| `channel`           | number  | 渠道 ID。Go 字段名为 `ChannelId`                                         |
| `channel_name`      | string  | 渠道名称，仅管理员列表补充；用户自查接口置空                             |
| `token_id`          | number  | 令牌 ID                                                                  |
| `group`             | string  | 请求使用分组                                                             |
| `ip`                | string  | 请求 IP。只有用户设置开启 IP 记录后，消费/错误日志才会写入               |
| `request_id`        | string  | 请求 ID                                                                  |
| `other`             | string  | JSON 字符串，保存计费、缓存、路由、审计等扩展信息                        |

用户自查接口会对 `other` 做脱敏：

| 处理                       | 说明                                |
| -------------------------- | ----------------------------------- |
| 删除 `other.admin_info`    | 隐藏管理员/路由调试信息             |
| 删除 `other.stream_status` | 隐藏流状态调试信息                  |
| `channel_name` 置空        | 不暴露渠道名称                      |
| `id` 重写                  | 改为当前页序号，不暴露数据库日志 ID |

## `other` 字段总览

`other` 在接口中是字符串，前端通过 `JSON.parse` 解析。消费日志、错误日志、任务日志、充值日志写入的字段不同。

### 通用文本消费字段

普通文本消费日志由 `GenerateTextOtherInfo` 和 `PostTextConsumeQuota` 生成。

| 字段                           | 类型     | 说明                                                       |
| ------------------------------ | -------- | ---------------------------------------------------------- |
| `model_ratio`                  | number   | 模型输入倍率                                               |
| `group_ratio`                  | number   | 分组倍率                                                   |
| `user_group_ratio`             | number   | 用户专属分组倍率。`-1` 或缺失表示不使用                    |
| `completion_ratio`             | number   | 输出倍率                                                   |
| `model_price`                  | number   | 固定价格。`-1` 表示按 tokens/倍率计费                      |
| `frt`                          | number   | 首字耗时，毫秒                                             |
| `request_path`                 | string   | 请求路径                                                   |
| `request_conversion`           | string[] | 请求格式转换链                                             |
| `claude`                       | boolean  | 最终上游请求格式为 Claude Messages，前端按 Claude 语义渲染 |
| `usage_semantic`               | string   | 用量语义，例如 `anthropic`                                 |
| `reasoning_effort`             | string   | reasoning effort 参数                                      |
| `is_model_mapped`              | boolean  | 是否发生模型映射                                           |
| `upstream_model_name`          | string   | 实际上游模型                                               |
| `is_system_prompt_overwritten` | boolean  | 是否覆盖系统提示词                                         |
| `po`                           | array    | 参数覆盖审计记录                                           |
| `stream_status`                | object   | 流式结束状态，管理员可见                                   |
| `admin_info`                   | object   | 管理员调试/审计信息，用户自查接口会移除                    |

### 缓存字段

| 字段                       | 类型   | 说明                                                                             |
| -------------------------- | ------ | -------------------------------------------------------------------------------- |
| `cache_tokens`             | number | 缓存读 tokens。来源是 `usage.prompt_tokens_details.cached_tokens` 等归一化后的值 |
| `cache_ratio`              | number | 缓存读倍率                                                                       |
| `cache_creation_tokens`    | number | 缓存写 tokens 的聚合值                                                           |
| `cache_creation_ratio`     | number | 缓存写倍率                                                                       |
| `cache_creation_tokens_5m` | number | Claude 5m 缓存写 tokens                                                          |
| `cache_creation_ratio_5m`  | number | Claude 5m 缓存写倍率                                                             |
| `cache_creation_tokens_1h` | number | Claude 1h 缓存写 tokens                                                          |
| `cache_creation_ratio_1h`  | number | Claude 1h 缓存写倍率                                                             |
| `cache_write_tokens`       | number | 后端归一化后的缓存写总量。前端优先使用该字段展示缓存写                           |
| `input_tokens_total`       | number | 非 Claude 格式下，上游明确返回且可可靠归一化的输入总 tokens                      |

缓存字段的来源：

1. 后端先从上游 usage 归一化到 `dto.Usage`。
2. `summary.CacheTokens` 读取 `usage.PromptTokensDetails.CachedTokens`。
3. `summary.CacheCreationTokens` 读取 `usage.PromptTokensDetails.CachedCreationTokens`。
4. `summary.CacheCreationTokens5m` 和 `summary.CacheCreationTokens1h` 读取 Claude 拆分缓存创建字段。
5. 写入日志时，缓存信息进入 `other` JSON 字符串。

Anthropic 语义注意点：

| 字段                                     | 口径                                    |
| ---------------------------------------- | --------------------------------------- |
| `prompt_tokens`                          | 非缓存输入 tokens，不包含缓存读和缓存写 |
| `other.cache_tokens`                     | 缓存读 tokens                           |
| `other.cache_creation_tokens`            | 缓存写 tokens 聚合值                    |
| `other.cache_creation_tokens_5m` / `_1h` | Claude 细分缓存写 tokens                |

## 前端缓存读写展示逻辑

缓存解析逻辑位于 `web/src/helpers/log.js`，使用位置主要在 `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx` 和 `web/src/hooks/usage-logs/useUsageLogsData.jsx`。

### 计算规则

前端先解析 `record.other`，再调用 `getUsageLogCacheSummary(other)`：

```js
cacheReadTokens = positiveNumber(other.cache_tokens);
normalizedCacheWriteTokens = positiveNumber(other.cache_write_tokens);
cacheCreationTokens = positiveNumber(other.cache_creation_tokens);
cacheCreationTokens5m = positiveNumber(other.cache_creation_tokens_5m);
cacheCreationTokens1h = positiveNumber(other.cache_creation_tokens_1h);

splitCacheWriteTokens = cacheCreationTokens5m + cacheCreationTokens1h;
cacheWriteTokens =
  normalizedCacheWriteTokens > 0
    ? normalizedCacheWriteTokens
    : splitCacheWriteTokens > 0
      ? Math.max(splitCacheWriteTokens, cacheCreationTokens)
      : cacheCreationTokens;
```

如果 `cacheReadTokens <= 0` 且 `cacheWriteTokens <= 0`，前端不展示缓存行。

### “输入”列展示

“输入”列第一行显示当前接口口径下的输入 tokens，第二行用小号灰色文字展示缓存：

| 条件               | 展示文案                     |
| ------------------ | ---------------------------- |
| 有缓存读且有缓存写 | `缓存读 {read} · 写 {write}` |
| 只有缓存读         | `缓存读 {read}`              |
| 只有缓存写         | `缓存写 {write}`             |
| 都没有             | 不显示第二行                 |

示例：

```text
1200
缓存读 8,000 · 写 2,000
```

这里的 `写` 来自前端计算的 `cacheWriteTokens`。新日志优先使用 `other.cache_write_tokens`；旧日志没有该字段时，回退到 `cache_creation_tokens_5m + cache_creation_tokens_1h` 或 `cache_creation_tokens`。

### `/v1/messages` 与 `/v1/chat/completions`

前端会根据 `other.request_path` 区分主要输入展示口径：

| 请求路径               | 输入列第一行                                                           | 输入列第二行                                                   |
| ---------------------- | ---------------------------------------------------------------------- | -------------------------------------------------------------- |
| `/v1/messages`         | `record.prompt_tokens`。Anthropic 语义下通常是非缓存输入 tokens        | `other.cache_tokens` 展示缓存读，`cacheWriteTokens` 展示缓存写 |
| `/v1/chat/completions` | 优先使用 `other.input_tokens_total`；缺失时回退 `record.prompt_tokens` | `other.cache_tokens` 展示缓存读，`cacheWriteTokens` 展示缓存写 |

这样可以同时兼容两类口径：

| 场景                                | 说明                                                                                                   |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Claude Messages 原生语义            | `prompt_tokens` 不包含缓存读/写，缓存读写必须单独展示                                                  |
| Chat Completions / OpenAI-like 语义 | `prompt_tokens` 可能已经是输入总量；当后端写入 `input_tokens_total` 时，前端以该字段作为输入列主值     |
| 旧日志                              | 没有 `input_tokens_total` 或 `cache_write_tokens` 时，继续使用 `prompt_tokens` 与缓存创建字段 fallback |

### 展开行展示

展开行中还会追加：

| 展开项            | 条件                     | 值                   |
| ----------------- | ------------------------ | -------------------- |
| `缓存 Tokens`     | `other.cache_tokens > 0` | `other.cache_tokens` |
| `缓存创建 Tokens` | `cacheWriteTokens > 0`   | `cacheWriteTokens`   |

如果只有 `cache_creation_tokens_5m` 或 `cache_creation_tokens_1h`，而聚合 `cache_creation_tokens` 为 0，展开行和“输入”列都会按 5m/1h fallback 显示缓存写。

### 详情列和计费过程

详情列和展开行的“计费过程”会根据 `other.claude` 选择不同渲染函数：

| 场景                    | 渲染逻辑                                                                     |
| ----------------------- | ---------------------------------------------------------------------------- |
| `other.claude === true` | 展示 Claude/Anthropic 语义，包含缓存读取、缓存创建、5m/1h 缓存创建价格或倍率 |
| 普通 OpenAI-like        | 展示普通输入、缓存读、缓存写、输出、图片/搜索/音频等计费拆分                 |
| 音频或 Realtime         | 展示文字输入/输出和音频输入/输出拆分                                         |
| 固定价格模型            | 以 `model_price` 为主，不按 tokens 展开缓存计费                              |

## 前端表格列与字段映射

| 表格列    | 来源字段                                                    | 说明                                         |
| --------- | ----------------------------------------------------------- | -------------------------------------------- |
| 时间      | `created_at`                                                | 前端转换为 `timestamp2string`                |
| 渠道      | `channel`, `channel_name`, `other.admin_info`               | 管理员可见。可显示多 key、渠道亲和性标记     |
| 用户      | `username`, `user_id`                                       | 管理员可见                                   |
| 令牌      | `token_name`                                                | 可点击复制                                   |
| 分组      | `group`，旧数据可回退 `other.group`                         | 使用分组                                     |
| 类型      | `type`                                                      | 映射为充值/消费/管理/系统/错误/退款          |
| 模型      | `model_name`, `other.upstream_model_name`                   | 模型映射时展示请求并计费模型和实际模型       |
| 用时/首字 | `use_time`, `is_stream`, `other.frt`, `other.stream_status` | 流式异常时管理员可见异常标记                 |
| 输入      | `prompt_tokens`, 缓存相关 `other.*`                         | 第一行输入 tokens，第二行缓存读/写           |
| 输出      | `completion_tokens`                                         | 输出 tokens 大于 0 时显示                    |
| 花费      | `quota`, `other.billing_source`                             | 订阅抵扣时显示“订阅抵扣”标签                 |
| IP        | `ip`                                                        | 开启 IP 记录后显示                           |
| 重试      | `other.admin_info.use_channel`                              | 管理员可见，展示渠道重试链                   |
| 详情      | `content`, `other.*`                                        | 紧凑展示倍率、价格、违规扣费、任务退款等摘要 |

## 展开行字段映射

展开行由前端根据 `record` 和 `other` 动态生成。

| 展开项                       | 来源字段                                                                           | 条件                        |
| ---------------------------- | ---------------------------------------------------------------------------------- | --------------------------- |
| 渠道信息                     | `channel`, `channel_name`                                                          | 管理员，类型 0/2/6          |
| Request ID                   | `request_id`                                                                       | 存在时                      |
| 语音输入/输出、文字输入/输出 | `other.audio_input`, `other.audio_output`, `other.text_input`, `other.text_output` | `other.ws` 或 `other.audio` |
| 缓存 Tokens                  | `other.cache_tokens`                                                               | 大于 0                      |
| 缓存创建 Tokens              | `cacheWriteTokens`                                                                 | 大于 0                      |
| 日志详情                     | 倍率/价格字段                                                                      | 消费日志                    |
| 其他详情                     | `content`                                                                          | 消费日志且内容存在          |
| 拦截原因                     | `other.reject_reason`                                                              | 管理员且存在                |
| 请求并计费模型               | `model_name`                                                                       | 模型映射                    |
| 实际模型                     | `other.upstream_model_name`                                                        | 模型映射                    |
| 计费过程                     | `prompt_tokens`, `completion_tokens`, `other.*`                                    | 非违规扣费消费日志          |
| Reasoning Effort             | `other.reasoning_effort`                                                           | 存在时                      |
| 任务ID                       | `other.task_id`                                                                    | 退款日志                    |
| 失败原因                     | `other.reason`                                                                     | 退款日志                    |
| 请求路径                     | `other.request_path`                                                               | 存在时                      |
| 流状态                       | `other.stream_status`                                                              | 管理员且存在                |
| 流错误详情                   | `other.stream_status.errors`                                                       | 管理员且非空                |
| 参数覆盖                     | `other.po`                                                                         | 非空数组                    |
| 订阅套餐                     | `other.subscription_plan_id`, `other.subscription_plan_title`                      | 订阅抵扣                    |
| 订阅实例                     | `other.subscription_id`                                                            | 订阅抵扣                    |
| 订阅结算                     | `subscription_pre_consumed`, `subscription_post_delta`, `subscription_consumed`    | 订阅抵扣                    |
| 订阅剩余                     | `subscription_remain`, `subscription_total`                                        | 订阅抵扣                    |
| 请求转换                     | `other.request_conversion`                                                         | 管理员，非充值/退款         |
| 计费模式                     | `other.admin_info.local_count_tokens`                                              | 管理员，非充值/退款         |
| 充值审计                     | `other.admin_info.payment_method` 等                                               | 管理员，充值日志            |
| 操作管理员                   | `other.admin_info.admin_username`, `other.admin_info.admin_id`                     | 管理员，管理日志            |

## 其他常见 `other` 字段

### 音频和 Realtime

| 字段                         | 说明                     |
| ---------------------------- | ------------------------ |
| `ws`                         | Realtime/WebSocket 日志  |
| `audio`                      | 音频接口日志             |
| `audio_input`                | 音频输入 tokens          |
| `audio_output`               | 音频输出 tokens          |
| `text_input`                 | 文本输入 tokens          |
| `text_output`                | 文本输出 tokens          |
| `audio_ratio`                | 音频输入倍率             |
| `audio_completion_ratio`     | 音频输出倍率             |
| `audio_input_seperate_price` | 音频输入是否使用独立价格 |
| `audio_input_token_count`    | 独立计价音频输入 tokens  |
| `audio_input_price`          | 音频输入独立价格         |

### 图片、搜索和工具调用

| 字段                          | 说明                 |
| ----------------------------- | -------------------- |
| `image`                       | 图片输入计费         |
| `image_ratio`                 | 图片输入倍率         |
| `image_output`                | 图片输入 tokens      |
| `web_search`                  | Web Search 计费      |
| `web_search_call_count`       | Web Search 调用次数  |
| `web_search_price`            | Web Search 单价      |
| `file_search`                 | File Search 计费     |
| `file_search_call_count`      | File Search 调用次数 |
| `file_search_price`           | File Search 单价     |
| `image_generation_call`       | 图片生成调用计费     |
| `image_generation_call_price` | 图片生成调用价格     |

### 订阅抵扣

| 字段                        | 说明                       |
| --------------------------- | -------------------------- |
| `billing_source`            | `wallet` 或 `subscription` |
| `billing_preference`        | 用户计费偏好               |
| `subscription_id`           | 订阅实例 ID                |
| `subscription_plan_id`      | 订阅套餐 ID                |
| `subscription_plan_title`   | 订阅套餐名称               |
| `subscription_pre_consumed` | 预扣额度                   |
| `subscription_post_delta`   | 请求完成后的结算差额       |
| `subscription_consumed`     | 本次最终抵扣额度           |
| `subscription_total`        | 订阅总额度                 |
| `subscription_used`         | 订阅已用额度               |
| `subscription_remain`       | 订阅剩余额度               |
| `wallet_quota_deducted`     | 订阅抵扣时为 0             |

### 任务和退款

| 字段                 | 说明             |
| -------------------- | ---------------- |
| `is_task`            | 异步任务消费日志 |
| `task_id`            | 任务 ID          |
| `reason`             | 退款或失败原因   |
| `pre_consumed_quota` | 任务预扣额度     |
| `actual_quota`       | 任务实际应扣额度 |

### 违规扣费

| 字段                   | 说明           |
| ---------------------- | -------------- |
| `violation_fee`        | 是否违规扣费   |
| `violation_fee_code`   | 违规扣费错误码 |
| `fee_quota`            | 实际扣费额度   |
| `base_amount`          | 基础扣费配置值 |
| `status_code`          | 上游错误状态码 |
| `upstream_error_type`  | 上游错误类型   |
| `upstream_error_code`  | 上游错误码     |
| `violation_fee_marker` | 违规扣费标记   |

### 错误日志

错误日志由 relay 错误处理写入，常见字段：

| 字段                          | 说明               |
| ----------------------------- | ------------------ |
| `request_path`                | 请求路径           |
| `error_type`                  | 错误类型           |
| `error_code`                  | 错误码             |
| `status_code`                 | HTTP 状态码        |
| `channel_id`                  | 出错渠道 ID        |
| `channel_name`                | 出错渠道名称       |
| `channel_type`                | 出错渠道类型       |
| `admin_info.use_channel`      | 已尝试渠道链       |
| `admin_info.is_multi_key`     | 是否多 key         |
| `admin_info.multi_key_index`  | 多 key 下标        |
| `admin_info.channel_affinity` | 渠道亲和性命中信息 |

### 管理员审计信息

`admin_info` 仅管理员接口保留。

| 字段                      | 说明                    |
| ------------------------- | ----------------------- |
| `use_channel`             | 请求实际使用/重试渠道链 |
| `is_multi_key`            | 是否使用多 key 渠道     |
| `multi_key_index`         | 多 key 下标             |
| `local_count_tokens`      | 是否本地计费            |
| `channel_affinity`        | 渠道亲和性信息          |
| `payment_method`          | 订单支付方式            |
| `callback_payment_method` | 回调支付方式            |
| `caller_ip`               | 支付回调调用者 IP       |
| `server_ip`               | 服务器 IP               |
| `version`                 | 系统版本                |
| `admin_id`                | 操作管理员 ID           |
| `admin_username`          | 操作管理员用户名        |

`admin_info.channel_affinity` 常见字段：

| 字段                   | 说明         |
| ---------------------- | ------------ |
| `rule_name` / `reason` | 亲和性规则名 |
| `using_group`          | 请求分组     |
| `selected_group`       | 选中渠道组   |
| `model`                | 模型         |
| `request_path`         | 请求路径     |
| `channel_id`           | 命中渠道     |
| `key_source`           | key 来源类型 |
| `key_key`              | key 名称     |
| `key_path`             | key 路径     |
| `key_hint`             | key 摘要     |
| `key_fp`               | key 指纹     |

## 统计接口字段

`GET /api/log/stat` 和 `GET /api/log/self/stat` 返回：

```json
{
  "quota": 123,
  "rpm": 10,
  "tpm": 4567
}
```

| 字段    | 说明                                                |
| ------- | --------------------------------------------------- |
| `quota` | 当前筛选条件下消费日志的 `quota` 汇总               |
| `rpm`   | 最近 60 秒消费日志数量                              |
| `tpm`   | 最近 60 秒 `prompt_tokens + completion_tokens` 汇总 |

统计接口只统计消费日志，即 `type = 2`。`tpm` 不额外叠加缓存读/写 tokens。

## 示例

列表接口中的一条缓存命中日志可能类似：

```json
{
  "id": 101,
  "user_id": 1,
  "created_at": 1760000000,
  "type": 2,
  "content": "",
  "username": "alice",
  "token_name": "default",
  "model_name": "claude-sonnet-4-5",
  "quota": 42,
  "prompt_tokens": 1200,
  "completion_tokens": 300,
  "use_time": 3,
  "is_stream": true,
  "channel": 8,
  "channel_name": "anthropic",
  "token_id": 12,
  "group": "default",
  "ip": "",
  "request_id": "req_xxx",
  "other": "{\"model_ratio\":1,\"group_ratio\":1,\"completion_ratio\":5,\"cache_tokens\":8000,\"cache_ratio\":0.1,\"cache_creation_tokens\":2000,\"cache_creation_ratio\":1.25,\"cache_creation_tokens_5m\":2000,\"cache_creation_ratio_5m\":1.25,\"claude\":true,\"usage_semantic\":\"anthropic\"}"
}
```

前端“输入”列会显示：

```text
1200
缓存读 8,000 · 写 2,000
```
