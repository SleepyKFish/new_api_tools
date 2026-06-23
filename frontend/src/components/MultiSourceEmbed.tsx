import { useState, useEffect, useCallback, useMemo } from 'react'
import { cn } from '../lib/utils'
import { Badge } from './ui/badge'
import { Card, CardContent } from './ui/card'
import { Loader2, Timer, AlertTriangle, Server, Globe2, Layers, Brain } from 'lucide-react'
import {
  OpenAI, Gemini, DeepSeek, SiliconCloud, Groq, Ollama, Claude, Mistral,
  Minimax, Baichuan, Moonshot, Spark, Qwen, Yi, Hunyuan, Stepfun, ZeroOne,
  Zhipu, ChatGLM, Cohere, Perplexity, Together, OpenRouter, Fireworks,
  Ai360, Doubao, Wenxin, Meta, Coze, Cerebras, Kimi, NewAPI, ZAI, ModelScope
} from '@lobehub/icons'

type Status = 'green' | 'yellow' | 'red'

interface ClassifiedSlot {
  slot: number
  start_time: number
  end_time: number
  total_requests: number
  success_count: number
  failure_count: number
  empty_count: number
  success_rate: number
  status: Status
}

interface ClassifiedModel {
  model_name: string
  display_name: string
  time_window: string
  total_requests: number
  success_count: number
  failure_count: number
  empty_count: number
  success_rate: number
  current_status: Status
  within_5s_rate: number | null
  within_10s_rate: number | null
  duration_within_10s_rate: number | null
  duration_within_20s_rate: number | null
  cache_hit_rate: number | null
  cache_write_rate: number | null
  cache_hit_tokens: number
  cache_write_tokens: number
  total_input_tokens: number
  total_output_tokens: number
  completion_tps: number | null
  timed_requests: number
  duration_timed_requests: number
  output_requests: number
  slot_data: ClassifiedSlot[]
  source_id?: string
  source_name?: string
}

interface AggregatedSource {
  source_id: string
  source_name: string
  source_type: string
  models: ClassifiedModel[]
  error?: string
}

interface AggregatedResult {
  sources: AggregatedSource[]
  time_window: string
  config: {
    time_window: string
    refresh_interval: number
    title: string
    theme: string
    agg_cache_ttl: number
  }
  updated_at: number
}

interface MultiSourceEmbedProps {
  themeOverride?: string
  refreshOverride?: number
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type IconComponent = React.ComponentType<any>

const MODEL_LOGO_MAP: Record<string, IconComponent> = {
  gpt: OpenAI,
  openai: OpenAI,
  o1: OpenAI,
  o3: OpenAI,
  chatgpt: OpenAI,
  'dall-e': OpenAI,
  whisper: OpenAI,
  tts: OpenAI,
  gemini: Gemini,
  gemma: Gemini,
  palm: Gemini,
  bard: Gemini,
  claude: Claude,
  anthropic: Claude,
  deepseek: DeepSeek,
  llama: Meta,
  meta: Meta,
  mistral: Mistral,
  mixtral: Mistral,
  codestral: Mistral,
  pixtral: Mistral,
  qwen: Qwen,
  tongyi: Qwen,
  yi: Yi,
  '01-ai': Yi,
  baichuan: Baichuan,
  glm: ChatGLM,
  chatglm: ChatGLM,
  zhipu: Zhipu,
  moonshot: Moonshot,
  kimi: Kimi,
  spark: Spark,
  xunfei: Spark,
  hunyuan: Hunyuan,
  tencent: Hunyuan,
  doubao: Doubao,
  bytedance: Doubao,
  wenxin: Wenxin,
  ernie: Wenxin,
  baidu: Wenxin,
  minimax: Minimax,
  abab: Minimax,
  stepfun: Stepfun,
  step: Stepfun,
  zeroone: ZeroOne,
  '01': ZeroOne,
  '360': Ai360,
  modelscope: ModelScope,
  groq: Groq,
  ollama: Ollama,
  cohere: Cohere,
  command: Cohere,
  perplexity: Perplexity,
  pplx: Perplexity,
  together: Together,
  openrouter: OpenRouter,
  fireworks: Fireworks,
  siliconcloud: SiliconCloud,
  silicon: SiliconCloud,
  cerebras: Cerebras,
  coze: Coze,
  newapi: NewAPI,
  zai: ZAI,
}

const STATUS_COLORS = {
  green: 'bg-green-500',
  yellow: 'bg-yellow-500',
  red: 'bg-red-500',
  empty: 'bg-gray-200 dark:bg-gray-700',
}

const STATUS_LABELS: Record<Status, string> = {
  green: '正常',
  yellow: '警告',
  red: '异常',
}

const apiUrl = import.meta.env.VITE_API_URL || ''

function getModelLogo(modelName: string): IconComponent | null {
  const lowerName = modelName.toLowerCase()
  for (const [pattern, Logo] of Object.entries(MODEL_LOGO_MAP)) {
    if (lowerName.includes(pattern)) return Logo
  }
  return null
}

function ModelLogo({ modelName, size = 20, className }: { modelName: string; size?: number; className?: string }) {
  const Logo = useMemo(() => getModelLogo(modelName), [modelName])
  if (Logo) return <Logo size={size} className={className} />
  return <Brain size={size} className={cn('text-muted-foreground', className)} />
}

function formatTime(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatDateTime(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatSlotAxisTime(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatSlotAxisEndTime(startTimestamp: number, endTimestamp: number): string {
  const startDate = new Date(startTimestamp * 1000)
  const endDate = new Date(endTimestamp * 1000)
  const isFullCalendarDay =
    endTimestamp - startTimestamp === 24 * 60 * 60 &&
    startDate.getHours() === 0 &&
    startDate.getMinutes() === 0 &&
    endDate.getHours() === 0 &&
    endDate.getMinutes() === 0

  return isFullCalendarDay ? '24:00' : formatSlotAxisTime(endTimestamp)
}

function formatCountdown(seconds: number): string {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return mins > 0 ? `${mins}:${secs.toString().padStart(2, '0')}` : `${secs}s`
}

function formatRate(value: number | null | undefined): string {
  return value == null ? '--' : `${value.toFixed(1)}%`
}

function formatPreciseRate(value: number | null | undefined): string {
  return value == null ? '--' : `${value.toFixed(2)}%`
}

function formatCacheWriteRate(value: number | null | undefined): string {
  return value == null ? 'N/A' : `${value.toFixed(2)}%`
}

function formatTps(value: number | null | undefined): string {
  return value == null ? '--' : value.toFixed(2)
}

function formatTokenCount(value: number | null | undefined): { compact: string; full: string } {
  const tokens = Math.max(0, Number(value || 0))
  const full = tokens.toLocaleString('zh-CN')
  const units = [
    { threshold: 1_000_000_000, suffix: 'B' },
    { threshold: 1_000_000, suffix: 'M' },
    { threshold: 1_000, suffix: 'K' },
  ]
  const unit = units.find((item) => tokens >= item.threshold)
  if (!unit) return { compact: tokens.toLocaleString('zh-CN'), full }

  const scaled = tokens / unit.threshold
  const digits = scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2
  return {
    compact: `${scaled.toFixed(digits).replace(/\.0+$/, '').replace(/(\.\d*[1-9])0+$/, '$1')}${unit.suffix}`,
    full,
  }
}

function formatTokenRatio(value: number | null | undefined, total: number | null | undefined): { compact: string; full: string } {
  const numerator = formatTokenCount(value)
  const denominator = formatTokenCount(total)
  return {
    compact: `${numerator.compact}/${denominator.compact}`,
    full: `${numerator.full} / ${denominator.full}`,
  }
}

function getTimeWindowLabel(value: string): string {
  const map: Record<string, string> = {
    '15m': '15分钟',
    '30m': '30分钟',
    '1h': '1小时',
    '6h': '6小时',
    '12h': '12小时',
    '24h': '24小时',
  }
  if (map[value]) return map[value]

  const match = value.match(/^(\d+)(min|m|h)$/)
  if (!match) return value || '24小时'
  return match[2] === 'h' ? `${match[1]}小时` : `${match[1]}分钟`
}

function statusRateClass(status: Status): string {
  return status === 'green'
    ? 'text-green-600 dark:text-green-400'
    : status === 'yellow'
      ? 'text-yellow-600 dark:text-yellow-400'
      : 'text-red-600 dark:text-red-400'
}

function avgRateClass(value: number): string {
  return value >= 95
    ? 'text-green-600 dark:text-green-400'
    : value >= 80
      ? 'text-yellow-600 dark:text-yellow-400'
      : 'text-red-600 dark:text-red-400'
}

function badgeVariant(status: Status): 'success' | 'warning' | 'destructive' {
  if (status === 'green') return 'success'
  if (status === 'yellow') return 'warning'
  return 'destructive'
}

function sourceTypeText(sourceType: string): string {
  return sourceType === 'local' ? '本机' : '远程'
}

export function MultiSourceEmbed({ themeOverride, refreshOverride }: MultiSourceEmbedProps) {
  void themeOverride
  const [data, setData] = useState<AggregatedResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [lastUpdate, setLastUpdate] = useState<Date | null>(null)
  const [countdown, setCountdown] = useState(0)

  const fetchData = useCallback(async () => {
    try {
      const resp = await fetch(`${apiUrl}/api/embed/multi-source/aggregated`)
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      const json = await resp.json()
      if (!json.success) throw new Error(json.error?.message || '加载失败')
      setData(json.data as AggregatedResult)
      setError(null)
      setLastUpdate(new Date())
    } catch (e) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  const refreshInterval = useMemo(() => {
    if (refreshOverride && refreshOverride > 0) return refreshOverride
    return data?.config?.refresh_interval || 60
  }, [refreshOverride, data?.config?.refresh_interval])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  useEffect(() => {
    if (refreshInterval <= 0) return

    let lastRefreshTime = Date.now()
    setCountdown(refreshInterval)

    const timer = setInterval(() => {
      setCountdown((prev) => {
        if (prev <= 1) {
          fetchData()
          lastRefreshTime = Date.now()
          return refreshInterval
        }
        return prev - 1
      })
    }, 1000)

    const handleVisibilityChange = () => {
      if (document.visibilityState !== 'visible') return
      const elapsed = Math.floor((Date.now() - lastRefreshTime) / 1000)
      if (elapsed >= refreshInterval) {
        fetchData()
        lastRefreshTime = Date.now()
        setCountdown(refreshInterval)
      } else {
        setCountdown(Math.max(1, refreshInterval - elapsed))
      }
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [refreshInterval, fetchData])

  const sources = useMemo(
    () => data?.sources?.filter((source) => source.models.length > 0 || source.error) || [],
    [data?.sources],
  )
  const modelStatuses = useMemo(() => sources.flatMap((source) => source.models), [sources])
  const timeWindow = data?.time_window || data?.config?.time_window || '24h'
  const totalRequests = modelStatuses.reduce((sum, model) => sum + model.total_requests, 0)
  const activeModels = modelStatuses.filter((model) => model.total_requests > 0)
  const avgSuccessRate = activeModels.length > 0
    ? +(activeModels.reduce((sum, model) => sum + model.success_rate, 0) / activeModels.length).toFixed(1)
    : 0
  const statusCounts = {
    green: modelStatuses.filter((model) => model.current_status === 'green').length,
    yellow: modelStatuses.filter((model) => model.current_status === 'yellow').length,
    red: modelStatuses.filter((model) => model.current_status === 'red').length,
  }

  if (loading && !data) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background text-foreground">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background text-foreground">
      <div className="max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-6 sm:py-8 space-y-5">
        <Card className="overflow-visible border-0 shadow-md">
          <div className="bg-gradient-to-r from-primary/5 via-primary/3 to-transparent">
            <CardContent className="p-5">
              <div className="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <div className="flex items-center gap-2.5">
                      <div className="w-8 h-8 rounded-lg bg-primary/10 flex items-center justify-center">
                        <Layers className="h-4 w-4 text-primary" />
                      </div>
                      <h2 className="text-xl font-semibold tracking-tight whitespace-nowrap">
                        {data?.config?.title || '模型状态监控'}
                      </h2>
                    </div>
                    <Badge variant="outline" className="font-normal shrink-0">
                      {getTimeWindowLabel(timeWindow)} 滑动窗口
                    </Badge>
                  </div>
                  <p className="text-sm text-muted-foreground mt-2 flex items-center flex-wrap gap-x-3 gap-y-1">
                    <span>信息源 <span className="font-semibold text-foreground">{sources.length}</span> 个</span>
                    <span className="text-muted-foreground/40">·</span>
                    <span>监控 <span className="font-semibold text-foreground">{modelStatuses.length}</span> 个模型</span>
                    {modelStatuses.length > 0 && (
                      <>
                        <span className="text-muted-foreground/40">·</span>
                        <span>总请求 <span className="font-semibold text-foreground tabular-nums">{totalRequests.toLocaleString()}</span></span>
                        <span className="text-muted-foreground/40">·</span>
                        <span>平均成功率 <span className={cn('font-semibold tabular-nums', avgRateClass(avgSuccessRate))}>{avgSuccessRate}%</span></span>
                        <span className="text-muted-foreground/40">·</span>
                        <span className="flex items-center gap-1.5">
                          <span className="inline-flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-green-500" /><span className="font-medium text-green-600 tabular-nums">{statusCounts.green}</span></span>
                          <span className="inline-flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-yellow-500" /><span className="font-medium text-yellow-600 tabular-nums">{statusCounts.yellow}</span></span>
                          <span className="inline-flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-red-500" /><span className="font-medium text-red-600 tabular-nums">{statusCounts.red}</span></span>
                        </span>
                      </>
                    )}
                    {lastUpdate && (
                      <>
                        <span className="text-muted-foreground/40">·</span>
                        <span>更新于 {lastUpdate.toLocaleTimeString('zh-CN')}</span>
                      </>
                    )}
                  </p>
                </div>

                {refreshInterval > 0 && (
                  <div className="inline-flex h-9 items-center gap-2 rounded-md border bg-background px-3 text-sm shadow-sm shrink-0">
                    <Timer className="h-4 w-4 text-muted-foreground" />
                    <span className="font-semibold text-primary tabular-nums">{formatCountdown(countdown)}</span>
                    <span className="text-muted-foreground">后刷新</span>
                  </div>
                )}
              </div>
            </CardContent>
          </div>
        </Card>

        {error && (
          <Card className="border-rose-200 bg-rose-50 dark:border-rose-900/50 dark:bg-rose-950/20">
            <CardContent className="p-4 flex items-center gap-2 text-sm text-rose-700 dark:text-rose-300">
              <AlertTriangle className="h-4 w-4" />
              数据加载失败: {error}
            </CardContent>
          </Card>
        )}

        {modelStatuses.length === 0 && !error ? (
          <Card>
            <CardContent className="py-16 text-center text-sm text-muted-foreground">
              暂无监控数据，请在后台配置信息源与模型
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-6">
            {sources.map((source) => (
              <SourceSection key={source.source_id} source={source} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function SourceSection({ source }: { source: AggregatedSource }) {
  const totalRequests = source.models.reduce((sum, model) => sum + model.total_requests, 0)
  const activeModels = source.models.filter((model) => model.total_requests > 0)
  const avgSuccessRate = activeModels.length > 0
    ? +(activeModels.reduce((sum, model) => sum + model.success_rate, 0) / activeModels.length).toFixed(1)
    : 0
  const SourceIcon = source.source_type === 'local' ? Server : Globe2

  return (
    <section className="space-y-3">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="w-7 h-7 rounded-lg bg-primary/10 text-primary flex items-center justify-center shrink-0">
            <SourceIcon className="h-4 w-4" />
          </div>
          <h3 className="text-base font-semibold truncate">{source.source_name}</h3>
          <Badge variant="outline" className="font-normal shrink-0">{sourceTypeText(source.source_type)}</Badge>
        </div>
        <p className="text-sm text-muted-foreground flex items-center flex-wrap gap-x-3 gap-y-1">
          <span>模型 <span className="font-semibold text-foreground tabular-nums">{source.models.length}</span></span>
          <span className="text-muted-foreground/40">·</span>
          <span>请求 <span className="font-semibold text-foreground tabular-nums">{totalRequests.toLocaleString()}</span></span>
          <span className="text-muted-foreground/40">·</span>
          <span>平均成功率 <span className={cn('font-semibold tabular-nums', avgRateClass(avgSuccessRate))}>{avgSuccessRate}%</span></span>
        </p>
      </div>

      {source.error ? (
        <Card className="border-amber-200 bg-amber-50 dark:border-amber-900/50 dark:bg-amber-950/20">
          <CardContent className="p-4 flex items-center gap-2 text-sm text-amber-700 dark:text-amber-300">
            <AlertTriangle className="h-4 w-4" />
            该源数据拉取失败: {source.error}
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
          {source.models.map((model) => (
            <MultiSourceModelCard key={`${source.source_id}:${model.model_name}`} model={model} />
          ))}
        </div>
      )}
    </section>
  )
}

function MultiSourceModelCard({ model }: { model: ClassifiedModel }) {
  const rateColorClass = statusRateClass(model.current_status)
  const failureRate = model.total_requests > 0
    ? (model.failure_count / model.total_requests * 100).toFixed(2)
    : '0.00'
  const emptyRate = model.total_requests > 0
    ? (model.empty_count / model.total_requests * 100).toFixed(2)
    : '0.00'
  const cardStatusClass = model.current_status === 'red'
    ? 'border-l-[3px] border-l-red-500 bg-red-500/[0.03]'
    : model.current_status === 'yellow'
      ? 'border-l-[3px] border-l-yellow-500 bg-yellow-500/[0.03]'
      : ''

  return (
    <Card className={cn('overflow-hidden transition-all duration-200 hover:shadow-lg hover:border-primary/20', cardStatusClass)}>
      <div className="px-4 pt-3 pb-3">
        <div className="flex items-start gap-2 mb-2.5 flex-wrap">
          <div className="flex items-center justify-center w-6 h-6 rounded-md bg-muted/50 flex-shrink-0">
            <ModelLogo modelName={model.model_name} size={16} />
          </div>
          <span className="text-sm font-medium leading-5 truncate min-w-0" title={model.model_name}>
            {model.model_name}
          </span>
          <Badge
            variant={badgeVariant(model.current_status)}
            className="text-[10px] px-1.5 py-0 h-5 flex-shrink-0"
          >
            {STATUS_LABELS[model.current_status]}
          </Badge>
          <TopStackedStats
            successRate={model.success_rate}
            successClassName={rateColorClass}
            failureRate={failureRate}
            emptyRate={emptyRate}
            totalRequests={model.total_requests}
            cacheHitTokens={model.cache_hit_tokens}
            cacheWriteTokens={model.cache_write_tokens}
            inputTokens={model.total_input_tokens}
            outputTokens={model.total_output_tokens}
          />
        </div>

        <div className="grid grid-cols-2 xl:grid-cols-3 gap-2 mb-3">
          <MetricPill label="首Token" detail="≤5s占比" value={formatRate(model.within_5s_rate)} tone="emerald" />
          <MetricPill label="首Token" detail="≤10s占比" value={formatRate(model.within_10s_rate)} tone="blue" />
          <MetricPill label="总耗时" detail="≤10s占比" value={formatRate(model.duration_within_10s_rate)} tone="emerald" />
          <MetricPill label="总耗时" detail="≤20s占比" value={formatRate(model.duration_within_20s_rate)} tone="blue" />
          <MetricPill label="缓存" detail="命中率" value={formatPreciseRate(model.cache_hit_rate)} tone="amber" />
          <MetricPill label="缓存" detail="写比例" value={formatCacheWriteRate(model.cache_write_rate)} tone="amber" />
          <MetricPill label="输出速度" detail="tok/s" value={formatTps(model.completion_tps)} tone="amber" />
        </div>

        <StatusSlotBar slots={model.slot_data} />
      </div>
    </Card>
  )
}

function MetricPill({
  label,
  detail,
  value,
  tone,
}: {
  label: string
  detail?: string
  value: string
  tone: 'emerald' | 'blue' | 'amber'
}) {
  const toneClass = tone === 'emerald'
    ? 'from-emerald-500/10 to-emerald-500/5 border-emerald-500/20'
    : tone === 'blue'
      ? 'from-blue-500/10 to-blue-500/5 border-blue-500/20'
      : 'from-amber-500/10 to-amber-500/5 border-amber-500/20'

  return (
    <div className={cn('rounded-lg border bg-gradient-to-br px-3 py-2 min-h-[62px] flex flex-col', toneClass)}>
      <div className="text-[11px] leading-tight text-muted-foreground min-h-[28px]">
        <div className="font-medium text-foreground/80">{label}</div>
        {detail && <div>{detail}</div>}
      </div>
      <div className="text-sm font-semibold tabular-nums mt-1">{value}</div>
    </div>
  )
}

function TopStackedStats({
  successRate,
  successClassName,
  failureRate,
  emptyRate,
  totalRequests,
  cacheHitTokens,
  cacheWriteTokens,
  inputTokens,
  outputTokens,
}: {
  successRate: number
  successClassName: string
  failureRate: string
  emptyRate: string
  totalRequests: number
  cacheHitTokens: number
  cacheWriteTokens: number
  inputTokens: number
  outputTokens: number
}) {
  const cacheHit = formatTokenRatio(cacheHitTokens, inputTokens)
  const cacheWrite = formatTokenRatio(cacheWriteTokens, inputTokens)
  const output = formatTokenCount(outputTokens)
  const total = formatTokenCount(inputTokens + outputTokens)
  const mainItems = [
    { title: '成功率', content: <span className={cn('font-semibold', successClassName)}>{successRate}%</span> },
    { title: '失败率', content: <><span className="text-muted-foreground/70">失 </span><span className="text-red-600 dark:text-red-400">{failureRate}%</span></> },
    { title: '空响应率', content: <><span className="text-muted-foreground/70">空 </span><span className="text-amber-600 dark:text-amber-400">{emptyRate}%</span></> },
    { title: '请求数', content: <span>{totalRequests.toLocaleString()}</span> },
  ]
  const tokenItems = [
    { title: cacheHit.full, content: <><span className="text-muted-foreground/70">命中 </span><span className="font-semibold text-amber-600 dark:text-amber-400">{cacheHit.compact}</span></> },
    { title: cacheWrite.full, content: <><span className="text-muted-foreground/70">写 </span><span className="font-semibold text-amber-600 dark:text-amber-400">{cacheWrite.compact}</span></> },
    { title: output.full, content: <><span className="text-muted-foreground/70">输出 </span><span className="font-semibold text-emerald-600 dark:text-emerald-400">{output.compact}</span></> },
    { title: total.full, content: <><span className="text-muted-foreground/70">总计 </span><span className="font-semibold text-sky-600 dark:text-sky-400">{total.compact}</span></> },
  ]

  return (
    <div className="ml-auto flex flex-col items-end gap-1 text-xs text-muted-foreground tabular-nums min-w-[240px] max-w-full">
      <InlineStatRow items={mainItems} />
      <InlineStatRow items={tokenItems} className="text-[11px]" />
    </div>
  )
}

function InlineStatRow({
  items,
  className,
}: {
  items: Array<{ title: string; content: React.ReactNode }>
  className?: string
}) {
  return (
    <div className={cn('flex flex-wrap justify-end gap-x-0 gap-y-0.5 leading-tight', className)}>
      {items.map((item, index) => (
        <span key={`${item.title}-${index}`} className="inline-flex items-center" title={item.title}>
          {index > 0 && <span className="mx-1 text-muted-foreground/40">·</span>}
          {item.content}
        </span>
      ))}
    </div>
  )
}

function StatusSlotBar({ slots }: { slots: ClassifiedSlot[] }) {
  const [hoveredSlot, setHoveredSlot] = useState<ClassifiedSlot | null>(null)
  const [tooltipPosition, setTooltipPosition] = useState({ x: 0, y: 0 })
  const [tooltipFlipped, setTooltipFlipped] = useState(false)

  const handleMouseEnter = (slot: ClassifiedSlot, event: React.MouseEvent) => {
    const rect = event.currentTarget.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const shouldFlip = rect.top < 100
    const clampedX = Math.max(120, Math.min(rect.left + rect.width / 2, viewportWidth - 120))
    setTooltipPosition({
      x: clampedX,
      y: shouldFlip ? rect.bottom + 10 : rect.top - 10,
    })
    setTooltipFlipped(shouldFlip)
    setHoveredSlot(slot)
  }

  const getTimeLabels = () => {
    if (slots.length === 0) return ['', '', '']

    const firstSlot = slots[0]
    const middleSlot = slots[Math.floor(slots.length / 2)]
    const lastSlot = slots[slots.length - 1]

    return [
      formatSlotAxisTime(firstSlot.start_time),
      formatSlotAxisTime(middleSlot.start_time),
      formatSlotAxisEndTime(firstSlot.start_time, lastSlot.end_time),
    ]
  }

  const timeLabels = getTimeLabels()

  return (
    <div className="relative">
      <div className="flex gap-[3px]">
        {slots.map((slot, index) => (
          <div
            key={index}
            className={cn(
              'flex-1 h-5 cursor-pointer transition-all hover:ring-1.5 hover:ring-primary hover:ring-offset-1 hover:scale-y-110',
              index === 0 ? 'rounded-l-md rounded-r-sm' :
                index === slots.length - 1 ? 'rounded-r-md rounded-l-sm' :
                  'rounded-sm',
              slot.total_requests === 0 ? STATUS_COLORS.empty : STATUS_COLORS[slot.status],
              'animate-in fade-in-0 duration-300',
            )}
            style={{ animationDelay: `${index * 15}ms` }}
            onMouseEnter={(e) => handleMouseEnter(slot, e)}
            onMouseLeave={() => setHoveredSlot(null)}
          />
        ))}
      </div>

      <div className="flex justify-between mt-1.5 text-[10px] text-muted-foreground/80">
        <span>{timeLabels[0]}</span>
        <span>{timeLabels[1]}</span>
        <span>{timeLabels[2]}</span>
      </div>

      {hoveredSlot && (
        <div
          className="fixed z-[9999] bg-popover border rounded-lg shadow-xl p-2.5 text-xs pointer-events-none animate-in fade-in-0 zoom-in-95 duration-150"
          style={{
            left: tooltipPosition.x,
            top: tooltipPosition.y,
            transform: tooltipFlipped ? 'translate(-50%, 0)' : 'translate(-50%, -100%)',
          }}
        >
          <div
            className={cn(
              'absolute left-1/2 -translate-x-1/2 w-2 h-2 bg-popover border rotate-45',
              tooltipFlipped
                ? '-top-1 border-b-0 border-r-0'
                : '-bottom-1 border-t-0 border-l-0',
            )}
          />
          <div className="font-medium mb-1.5">
            {formatDateTime(hoveredSlot.start_time)} - {formatTime(hoveredSlot.end_time)}
          </div>
          <div className="space-y-0.5 text-muted-foreground">
            <TooltipRow label="请求:" value={hoveredSlot.total_requests.toLocaleString()} />
            <TooltipRow label="成功:" value={hoveredSlot.success_count.toLocaleString()} valueClassName="text-green-600 dark:text-green-400" />
            <TooltipRow
              label="成功率:"
              value={`${hoveredSlot.success_rate}%`}
              valueClassName={statusRateClass(hoveredSlot.status)}
            />
            <TooltipRow
              label="失败率:"
              value={`${hoveredSlot.total_requests > 0 ? (hoveredSlot.failure_count / hoveredSlot.total_requests * 100).toFixed(2) : '0.00'}%`}
              valueClassName="text-red-600 dark:text-red-400"
            />
            <TooltipRow
              label="空响应率:"
              value={`${hoveredSlot.total_requests > 0 ? (hoveredSlot.empty_count / hoveredSlot.total_requests * 100).toFixed(2) : '0.00'}%`}
              valueClassName="text-amber-600 dark:text-amber-400"
            />
          </div>
        </div>
      )}
    </div>
  )
}

function TooltipRow({
  label,
  value,
  valueClassName,
}: {
  label: string
  value: string
  valueClassName?: string
}) {
  return (
    <div className="flex justify-between gap-4">
      <span>{label}</span>
      <span className={cn('font-medium text-foreground tabular-nums', valueClassName)}>{value}</span>
    </div>
  )
}

export default MultiSourceEmbed
