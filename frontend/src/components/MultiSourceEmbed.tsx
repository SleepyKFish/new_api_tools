import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { cn } from '../lib/utils'
import { Loader2, Timer, AlertTriangle, ShieldAlert, FileWarning, Server } from 'lucide-react'
import {
  OpenAI, Gemini, DeepSeek, SiliconCloud, Groq, Ollama, Claude, Mistral,
  Minimax, Baichuan, Moonshot, Spark, Qwen, Yi, Hunyuan, Stepfun, ZeroOne,
  Zhipu, ChatGLM, Cohere, Perplexity, Together, OpenRouter, Fireworks,
  Ai360, Doubao, Wenxin, Meta, Coze, Cerebras, Kimi, NewAPI, ZAI, ModelScope
} from '@lobehub/icons'

// ============================================================================
// 类型
// ============================================================================

interface ClassifiedSlot {
  slot: number
  start_time: number
  end_time: number
  total_requests: number
  success_count: number
  failure_count: number
  empty_count: number
  model_error_count: number
  format_error_count: number
  rate_limit_count: number
  success_rate: number
  model_success_rate: number
  status: 'green' | 'yellow' | 'red'
  model_status: 'green' | 'yellow' | 'red'
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
  model_success_rate: number
  model_error_count: number
  format_error_count: number
  rate_limit_count: number
  current_status: 'green' | 'yellow' | 'red'
  model_current_status: 'green' | 'yellow' | 'red'
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

// ============================================================================
// 主题 (紧凑版: 深色/浅色两套，复用现有状态色语义)
// ============================================================================

const LIGHT_THEMES = new Set([
  'daylight', 'minimal', 'cupertino', 'material', 'openai', 'stripe', 'github',
])

interface ThemeStyle {
  container: string
  headerTitle: string
  headerSub: string
  badge: string
  sourceHeader: string
  card: string
  cardHover: string
  modelName: string
  statsLabel: string
  statsValue: string
  divider: string
  emptyBar: string
  timeLabel: string
  tooltip: string
  tooltipLabel: string
  tooltipValue: string
  loader: string
}

const DARK_STYLE: ThemeStyle = {
  container: 'min-h-screen bg-[#0d1117] text-gray-100 p-5 sm:p-6',
  headerTitle: 'text-2xl font-bold text-white tracking-tight',
  headerSub: 'text-sm text-gray-500 mt-1',
  badge: 'bg-[#161b22] border border-gray-800 text-gray-300',
  sourceHeader: 'text-gray-300 border-gray-800',
  card: 'bg-[#161b22] border border-gray-800/80 rounded-xl p-4 transition-all duration-300',
  cardHover: 'hover:border-gray-700 hover:bg-[#1c2129]',
  modelName: 'font-semibold text-white truncate',
  statsLabel: 'text-gray-500',
  statsValue: 'text-white font-semibold',
  divider: 'text-gray-700',
  emptyBar: 'bg-gray-700',
  timeLabel: 'text-[11px] text-gray-600 font-mono',
  tooltip: 'bg-[#1c2128] border border-gray-700 rounded-xl shadow-2xl p-3.5 z-[9999] text-xs',
  tooltipLabel: 'text-gray-400',
  tooltipValue: 'text-white font-medium',
  loader: 'text-gray-500',
}

const LIGHT_STYLE: ThemeStyle = {
  container: 'min-h-screen bg-gradient-to-b from-slate-50 to-slate-100 text-slate-900 p-5 sm:p-6',
  headerTitle: 'text-2xl font-bold text-slate-800 tracking-tight',
  headerSub: 'text-sm text-slate-500 mt-1',
  badge: 'bg-white border border-slate-200 text-slate-600 shadow-sm',
  sourceHeader: 'text-slate-600 border-slate-200',
  card: 'bg-white border border-slate-200 rounded-xl p-4 shadow-sm transition-all duration-300',
  cardHover: 'hover:shadow-md hover:border-slate-300',
  modelName: 'font-semibold text-slate-800 truncate',
  statsLabel: 'text-slate-500',
  statsValue: 'text-slate-800 font-semibold',
  divider: 'text-slate-300',
  emptyBar: 'bg-slate-300',
  timeLabel: 'text-[11px] text-slate-400 font-mono',
  tooltip: 'bg-white border border-slate-200 rounded-xl shadow-xl p-3.5 z-[9999] text-xs',
  tooltipLabel: 'text-slate-500',
  tooltipValue: 'text-slate-800 font-medium',
  loader: 'text-slate-400',
}

function resolveStyle(theme: string): ThemeStyle {
  return LIGHT_THEMES.has(theme) ? LIGHT_STYLE : DARK_STYLE
}

const STATUS_BAR: Record<string, string> = {
  green: 'bg-emerald-500',
  yellow: 'bg-amber-500',
  red: 'bg-rose-500',
}
const STATUS_BADGE: Record<string, string> = {
  green: 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30',
  yellow: 'bg-amber-500/15 text-amber-400 border border-amber-500/30',
  red: 'bg-rose-500/15 text-rose-400 border border-rose-500/30',
}
const STATUS_LABEL: Record<string, string> = {
  green: '正常',
  yellow: '波动',
  red: '异常',
}

// ============================================================================
// 模型 Logo
// ============================================================================

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type IconComponent = React.ComponentType<any>

const MODEL_LOGO_MAP: Record<string, IconComponent> = {
  'gpt': OpenAI, 'openai': OpenAI, 'o1': OpenAI, 'o3': OpenAI, 'chatgpt': OpenAI,
  'dall-e': OpenAI, 'whisper': OpenAI, 'tts': OpenAI,
  'gemini': Gemini, 'gemma': Gemini, 'palm': Gemini, 'bard': Gemini,
  'claude': Claude, 'anthropic': Claude,
  'deepseek': DeepSeek,
  'llama': Meta, 'meta': Meta,
  'mistral': Mistral, 'mixtral': Mistral, 'codestral': Mistral, 'pixtral': Mistral,
  'qwen': Qwen, 'tongyi': Qwen, 'yi': Yi, '01-ai': Yi, 'baichuan': Baichuan,
  'glm': ChatGLM, 'chatglm': ChatGLM, 'zhipu': Zhipu, 'moonshot': Moonshot, 'kimi': Kimi,
  'spark': Spark, 'xunfei': Spark, 'hunyuan': Hunyuan, 'tencent': Hunyuan,
  'doubao': Doubao, 'bytedance': Doubao, 'wenxin': Wenxin, 'ernie': Wenxin, 'baidu': Wenxin,
  'minimax': Minimax, 'abab': Minimax, 'stepfun': Stepfun, 'step': Stepfun,
  'zeroone': ZeroOne, '01': ZeroOne, '360': Ai360, 'modelscope': ModelScope,
  'groq': Groq, 'ollama': Ollama, 'cohere': Cohere, 'command': Cohere,
  'perplexity': Perplexity, 'pplx': Perplexity, 'together': Together,
  'openrouter': OpenRouter, 'fireworks': Fireworks, 'siliconcloud': SiliconCloud,
  'silicon': SiliconCloud, 'cerebras': Cerebras, 'coze': Coze, 'newapi': NewAPI, 'zai': ZAI,
}

function ModelLogo({ modelName, size = 18 }: { modelName: string; size?: number }) {
  const lower = modelName.toLowerCase()
  let Icon: IconComponent | undefined
  for (const [key, comp] of Object.entries(MODEL_LOGO_MAP)) {
    if (lower.includes(key)) {
      Icon = comp
      break
    }
  }
  if (!Icon) {
    return <span className="text-current opacity-40 text-xs font-bold">{modelName.slice(0, 2).toUpperCase()}</span>
  }
  try {
    return <Icon size={size} />
  } catch {
    return <span className="text-current opacity-40 text-xs font-bold">{modelName.slice(0, 2).toUpperCase()}</span>
  }
}

// ============================================================================
// 工具
// ============================================================================

const apiUrl = import.meta.env.VITE_API_URL || ''

function pct(part: number, total: number): string {
  if (!total || total <= 0) return '0.00'
  return ((part / total) * 100).toFixed(2)
}

function timeLabelsFor(window: string): [string, string, string] {
  switch (window) {
    case '15m': return ['15分钟前', '7分钟前', '现在']
    case '30m': return ['30分钟前', '15分钟前', '现在']
    case '1h': return ['60分钟前', '30分钟前', '现在']
    case '6h': return ['6小时前', '3小时前', '现在']
    case '12h': return ['12小时前', '6小时前', '现在']
    case '24h': return ['24小时前', '12小时前', '现在']
    default: {
      const m = window.match(/^(\d+)(min|m|h)$/)
      if (m) {
        const unit = m[2] === 'h' ? '小时' : '分钟'
        return [`${m[1]}${unit}前`, '', '现在']
      }
      return ['', '', '现在']
    }
  }
}

// ============================================================================
// 主组件
// ============================================================================

interface HoverState {
  slot: ClassifiedSlot
  x: number
  y: number
}

export function MultiSourceEmbed({ themeOverride, refreshOverride }: MultiSourceEmbedProps) {
  const [data, setData] = useState<AggregatedResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [countdown, setCountdown] = useState(0)
  const [hover, setHover] = useState<HoverState | null>(null)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const fetchData = useCallback(async () => {
    try {
      const resp = await fetch(`${apiUrl}/api/embed/multi-source/aggregated`)
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      const json = await resp.json()
      if (!json.success) throw new Error(json.error?.message || '加载失败')
      setData(json.data as AggregatedResult)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  // 刷新间隔: URL 覆盖 > 后端配置
  const refreshInterval = useMemo(() => {
    if (refreshOverride && refreshOverride > 0) return refreshOverride
    return data?.config?.refresh_interval || 60
  }, [refreshOverride, data?.config?.refresh_interval])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  // 自动刷新 + 倒计时
  useEffect(() => {
    if (refreshInterval <= 0) return
    setCountdown(refreshInterval)
    if (timerRef.current) clearInterval(timerRef.current)
    timerRef.current = setInterval(() => {
      setCountdown((c) => {
        if (c <= 1) {
          fetchData()
          return refreshInterval
        }
        return c - 1
      })
    }, 1000)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [refreshInterval, fetchData])

  const theme = themeOverride || data?.config?.theme || 'obsidian'
  const styles = resolveStyle(theme)
  const title = data?.config?.title || '模型监控'
  const window_ = data?.time_window || data?.config?.time_window || ''

  if (loading) {
    return (
      <div className={cn(styles.container, 'flex items-center justify-center')}>
        <Loader2 className={cn('w-8 h-8 animate-spin', styles.loader)} />
      </div>
    )
  }

  const sources = data?.sources?.filter((s) => s.models.length > 0 || s.error) || []
  const hasAnyModel = sources.some((s) => s.models.length > 0)

  return (
    <div className={styles.container}>
      {/* Header */}
      <div className="flex items-start justify-between mb-6 gap-4 flex-wrap">
        <div>
          <h1 className={styles.headerTitle}>{title}</h1>
          <p className={styles.headerSub}>
            仅展示最近 {windowText(window_)} 数据 · 成功率按模型侧错误计算
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span className={cn('px-3 py-1.5 text-xs rounded-lg font-medium', styles.badge)}>
            {windowText(window_)}
          </span>
          {refreshInterval > 0 && (
            <span className={cn('flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg font-mono', styles.badge)}>
              <Timer className="w-3.5 h-3.5" />
              {countdown}s
            </span>
          )}
        </div>
      </div>

      {error && (
        <div className="mb-4 flex items-center gap-2 px-4 py-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-rose-400 text-sm">
          <AlertTriangle className="w-4 h-4" />
          数据加载失败: {error}
        </div>
      )}

      {!hasAnyModel && !error && (
        <div className={cn('text-center py-20 text-sm', styles.loader)}>
          暂无监控数据，请在后台配置信息源与模型
        </div>
      )}

      {/* Sources */}
      <div className="space-y-8">
        {sources.map((source) => (
          <SourceSection
            key={source.source_id}
            source={source}
            window={window_}
            styles={styles}
            onHover={setHover}
            onLeave={() => setHover(null)}
          />
        ))}
      </div>

      {/* Legend */}
      <Legend styles={styles} />

      {/* Tooltip */}
      {hover && <SlotTooltip hover={hover} styles={styles} />}
    </div>
  )
}

function windowText(window: string): string {
  if (!window) return '—'
  const map: Record<string, string> = {
    '15m': '15 分钟', '30m': '30 分钟', '1h': '1 小时',
    '6h': '6 小时', '12h': '12 小时', '24h': '24 小时',
  }
  if (map[window]) return map[window]
  const m = window.match(/^(\d+)(min|m|h)$/)
  if (m) return `${m[1]} ${m[2] === 'h' ? '小时' : '分钟'}`
  return window
}

// ============================================================================
// 源分组
// ============================================================================

function SourceSection({
  source, window, styles, onHover, onLeave,
}: {
  source: AggregatedSource
  window: string
  styles: ThemeStyle
  onHover: (h: HoverState) => void
  onLeave: () => void
}) {
  return (
    <section>
      <div className={cn('flex items-center gap-2 mb-3 pb-2 border-b', styles.sourceHeader)}>
        <Server className="w-4 h-4 opacity-60" />
        <h2 className="text-sm font-semibold tracking-wide">{source.source_name}</h2>
        <span className="text-xs opacity-50">
          {source.source_type === 'local' ? '本机' : '远程'} · {source.models.length} 个模型
        </span>
      </div>

      {source.error ? (
        <div className="flex items-center gap-2 px-4 py-3 rounded-lg bg-amber-500/10 border border-amber-500/30 text-amber-500 text-sm">
          <AlertTriangle className="w-4 h-4" />
          该源数据拉取失败: {source.error}
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
          {source.models.map((model) => (
            <ModelCard
              key={`${source.source_id}:${model.model_name}`}
              model={model}
              window={window}
              styles={styles}
              onHover={onHover}
              onLeave={onLeave}
            />
          ))}
        </div>
      )}
    </section>
  )
}

// ============================================================================
// 模型卡片
// ============================================================================

function ModelCard({
  model, window, styles, onHover, onLeave,
}: {
  model: ClassifiedModel
  window: string
  styles: ThemeStyle
  onHover: (h: HoverState) => void
  onLeave: () => void
}) {
  const status = model.model_current_status || model.current_status
  const labels = timeLabelsFor(window)

  return (
    <div className={cn(styles.card, styles.cardHover, 'relative')}>
      {/* Header */}
      <div className="flex items-center justify-between mb-3 gap-2">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="flex items-center justify-center w-7 h-7 rounded-md bg-current/5 flex-shrink-0">
            <ModelLogo modelName={model.model_name} size={18} />
          </div>
          <h3 className={styles.modelName} title={model.model_name}>{model.model_name}</h3>
          <span className={cn('px-2 py-0.5 text-xs rounded-full font-medium flex-shrink-0', STATUS_BADGE[status])}>
            {STATUS_LABEL[status]}
          </span>
        </div>
        <div className="text-right flex-shrink-0">
          <div className={cn('text-lg font-bold leading-none', textForStatus(status))}>
            {(model.model_success_rate ?? model.success_rate).toFixed(2)}%
          </div>
          <div className={cn('text-[11px] mt-0.5', styles.statsLabel)}>模型侧成功率</div>
        </div>
      </div>

      {/* 错误拆分 */}
      <div className="flex items-center flex-wrap gap-x-3 gap-y-1 mb-3 text-xs">
        <Metric label="请求" value={model.total_requests.toLocaleString()} styles={styles} />
        <span className={styles.divider}>·</span>
        <ErrorMetric
          icon={<ShieldAlert className="w-3 h-3" />}
          label="模型侧"
          count={model.model_error_count}
          rate={pct(model.model_error_count, model.total_requests)}
          color="text-rose-500"
        />
        <ErrorMetric
          icon={<FileWarning className="w-3 h-3" />}
          label="格式"
          count={model.format_error_count}
          rate={pct(model.format_error_count, model.total_requests)}
          color="text-amber-500"
        />
        <ErrorMetric
          icon={<Timer className="w-3 h-3" />}
          label="限速"
          count={model.rate_limit_count}
          rate={pct(model.rate_limit_count, model.total_requests)}
          color="text-sky-500"
        />
      </div>

      {/* Slot 时间条 */}
      <div className="flex gap-0.5 h-7">
        {model.slot_data.map((slot, i) => {
          const slotStatus = slot.model_status || slot.status
          return (
            <div
              key={i}
              className={cn(
                'flex-1 rounded-sm cursor-pointer transition-all duration-200',
                'hover:ring-2 hover:ring-white/30 hover:scale-y-110 origin-bottom',
                slot.total_requests === 0 ? styles.emptyBar : STATUS_BAR[slotStatus],
              )}
              onMouseEnter={(e) => {
                const rect = e.currentTarget.getBoundingClientRect()
                onHover({ slot, x: rect.left + rect.width / 2, y: rect.top })
              }}
              onMouseLeave={onLeave}
            />
          )
        })}
      </div>

      {/* 时间标签 */}
      <div className={cn('flex justify-between mt-1.5', styles.timeLabel)}>
        <span>{labels[0]}</span>
        <span>{labels[1]}</span>
        <span>{labels[2]}</span>
      </div>
    </div>
  )
}

function textForStatus(status: string): string {
  switch (status) {
    case 'green': return 'text-emerald-500'
    case 'yellow': return 'text-amber-500'
    default: return 'text-rose-500'
  }
}

function Metric({ label, value, styles }: { label: string; value: string; styles: ThemeStyle }) {
  return (
    <span className={styles.statsLabel}>
      <span className={styles.statsValue}>{value}</span> {label}
    </span>
  )
}

function ErrorMetric({
  icon, label, count, rate, color,
}: {
  icon: React.ReactNode
  label: string
  count: number
  rate: string
  color: string
}) {
  return (
    <span className={cn('flex items-center gap-1', count > 0 ? color : 'opacity-40')}>
      {icon}
      {label} {count > 0 ? `${rate}%` : '0%'}
    </span>
  )
}

// ============================================================================
// Tooltip
// ============================================================================

function SlotTooltip({ hover, styles }: { hover: HoverState; styles: ThemeStyle }) {
  const { slot, x, y } = hover
  const fmtTime = (ts: number) => {
    const d = new Date(ts * 1000)
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }
  return (
    <div
      className={cn('fixed pointer-events-none', styles.tooltip)}
      style={{
        left: x,
        top: y - 12,
        transform: 'translate(-50%, -100%)',
        minWidth: 180,
      }}
    >
      <div className={cn('font-semibold mb-2 pb-1.5 border-b', styles.tooltipLabel, 'border-current/10')}>
        {fmtTime(slot.start_time)} - {fmtTime(slot.end_time)}
      </div>
      <div className="space-y-1">
        <Row label="请求" value={slot.total_requests.toLocaleString()} styles={styles} />
        <Row label="模型侧成功率" value={`${(slot.model_success_rate ?? slot.success_rate).toFixed(2)}%`} styles={styles} valueClass="text-emerald-500" />
        <Row label="模型侧错误" value={`${slot.model_error_count} (${pct(slot.model_error_count, slot.total_requests)}%)`} styles={styles} valueClass="text-rose-500" />
        <Row label="格式错误" value={`${slot.format_error_count} (${pct(slot.format_error_count, slot.total_requests)}%)`} styles={styles} valueClass="text-amber-500" />
        <Row label="限速" value={`${slot.rate_limit_count} (${pct(slot.rate_limit_count, slot.total_requests)}%)`} styles={styles} valueClass="text-sky-500" />
      </div>
    </div>
  )
}

function Row({ label, value, styles, valueClass }: { label: string; value: string; styles: ThemeStyle; valueClass?: string }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <span className={styles.tooltipLabel}>{label}</span>
      <span className={cn(styles.tooltipValue, valueClass)}>{value}</span>
    </div>
  )
}

// ============================================================================
// 图例
// ============================================================================

function Legend({ styles }: { styles: ThemeStyle }) {
  return (
    <div className={cn('flex items-center flex-wrap gap-x-5 gap-y-2 mt-8 pt-4 border-t text-xs', styles.sourceHeader)}>
      <span className="flex items-center gap-1.5">
        <span className="w-3 h-3 rounded bg-emerald-500" /> 正常 (≥95%)
      </span>
      <span className="flex items-center gap-1.5">
        <span className="w-3 h-3 rounded bg-amber-500" /> 波动 (80-95%)
      </span>
      <span className="flex items-center gap-1.5">
        <span className="w-3 h-3 rounded bg-rose-500" /> 异常 (&lt;80%)
      </span>
      <span className="flex items-center gap-1.5">
        <span className={cn('w-3 h-3 rounded', styles.emptyBar)} /> 无请求
      </span>
      <span className={cn('flex items-center gap-1.5', styles.statsLabel)}>
        成功率 = 成功 / (成功 + 模型侧错误 + 限速)，格式错误不计入
      </span>
    </div>
  )
}
