/**
 * WeeklyPatternAnalytics —— 「周内规律分析（近4周 · 按星期几）」
 *
 * 一个卡片,三个上下堆叠的子图共用「星期几」横坐标(周一~周日):
 * - 图1:每周·每天的额度花费(¥)
 * - 图2:每周·每天的 Token 总量
 * - 图3:每周·每天的缓存命中率(%)
 *
 * 每张图内按星期几分组,组内 4 根柱分别代表最近 4 个日历周
 * (本周 / 上周 / 2周前 / 3周前),便于横向对比「同一个星期几在不同周的高低」。
 *
 * 数据:dailyTrends —— 近 28 天的按天趋势(date=YYYY-MM-DD、quota_used、
 * prompt_tokens/input_tokens、completion_tokens、cache_hit_tokens、cache_write_tokens)。
 * 注意:部分部署(大型系统走 quota_data 聚合)下 token/缓存字段为 0,
 * 此时 Token 图与命中率图会自动断开该点,不做误导性展示。
 */

import { useMemo } from 'react'
import ReactECharts from 'echarts-for-react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { CalendarRange, BarChart3 } from 'lucide-react'
import { formatTokens, QUOTA_PER_YUAN } from '../lib/format'

interface DailyTrend {
  date?: string
  timestamp?: number
  request_count: number
  quota_used: number
  unique_users?: number
  prompt_tokens?: number
  input_tokens?: number
  completion_tokens?: number
  cache_hit_tokens?: number
  cache_write_tokens?: number
}

export interface WeeklyPatternAnalyticsProps {
  dailyTrends: DailyTrend[]
  loading?: boolean
}

const WEEK_LABELS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']
// 最近 4 周从旧到新的配色(越新越深/越突出),第 4 条=本周用主色 sky。
const WEEK_COLORS = ['#cbd5e1', '#93c5fd', '#38bdf8', '#0284c7']

/** 把周一时间戳(ms)格式化为「MM-DD ~ MM-DD」的整周区间(周一~周日,补零对齐)。 */
function weekRangeLabel(mondayMs: number): string {
  const mon = new Date(mondayMs)
  const sun = new Date(mondayMs)
  sun.setDate(sun.getDate() + 6)
  const p = (n: number) => String(n).padStart(2, '0')
  const fmt = (d: Date) => `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  return `${fmt(mon)} ~ ${fmt(sun)}`
}

/**
 * 给定周一周一的时间戳和星期几索引(0=周一~6=周日),
 * 返回「MM-DD」——这一行的精确日期,tooltip 用以消除范围歧义。
 */
function specificDateLabel(mondayMs: number, dayIdx: number): string {
  const d = new Date(mondayMs + dayIdx * 86400000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/**
 * 给定在 weeks[] 里的下标与总周数,返回相对周次标签:
 * 总数=4 时,index 3 -> 本周 / 2 -> 上周 / 1 -> 2 周前 / 0 -> 3 周前。
 * 总数 < 4 时按相同规则向前递推,不足 4 周的旧数据不会硬填到 3 周前。
 */
function relativeWeekLabel(index: number, total: number): string {
  const diff = total - 1 - index // 0 = 最新(本周)
  if (diff <= 0) return '本周'
  return `${diff}周前`
}

/**
 * 环比涨跌标签:当前值相对前一周同一天的百分比变化。
 * 上升红(↑)、下降绿(↓)、持平灰;任一端缺数据显示占位「—」。
 */
function deltaLabel(prev: number | null, curr: number | null): string {
  if (prev === null || curr === null || prev === 0) {
    return `<span style="color:#cbd5e1">—</span>`
  }
  const pct = ((curr - prev) / prev) * 100
  if (Math.abs(pct) < 0.05) return `<span style="color:#94a3b8">0%</span>`
  const up = pct > 0
  const color = up ? '#ef4444' : '#10b981'
  const arrow = up ? '↑' : '↓'
  return `<span style="color:${color}">${arrow}${Math.abs(pct).toFixed(1)}%</span>`
}

/** 由 timestamp 或 date 取得本地 Date;无法解析返回 null。 */
function trendDate(d: DailyTrend): Date | null {
  if (d.timestamp) return new Date(d.timestamp * 1000)
  if (d.date) {
    const parts = d.date.split('-').map(Number)
    if (parts.length === 3 && parts.every(Number.isFinite)) {
      return new Date(parts[0], parts[1] - 1, parts[2])
    }
  }
  return null
}

/** 周一=0 … 周日=6(把 JS 的 0=周日 归一化到周一起始)。 */
function mondayIndex(date: Date): number {
  const dow = date.getDay() // 0=Sun
  return dow === 0 ? 6 : dow - 1
}

/** 该日期所在周的周一 0 点的本地时间戳(毫秒),用作周分组键。 */
function weekStartKey(date: Date): number {
  const d = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  d.setDate(d.getDate() - mondayIndex(d))
  return d.getTime()
}

interface WeekBucket {
  key: number // 周一 0 点时间戳(ms)
  cost: (number | null)[] // 长度 7,周一→周日,¥
  tokens: (number | null)[]
  hitRate: (number | null)[]
}

interface ChartModel {
  weeks: WeekBucket[] // 从旧到新,最多 4 个
  hasToken: boolean // 是否有任何 token 数据(决定 Token/命中率图是否有意义)
}

function buildModel(dailyTrends: DailyTrend[]): ChartModel {
  const byWeek = new Map<number, WeekBucket>()

  for (const d of dailyTrends) {
    const date = trendDate(d)
    if (!date) continue
    const key = weekStartKey(date)
    const dayIdx = mondayIndex(date)

    let bucket = byWeek.get(key)
    if (!bucket) {
      bucket = {
        key,
        cost: Array(7).fill(null),
        tokens: Array(7).fill(null),
        hitRate: Array(7).fill(null),
      }
      byWeek.set(key, bucket)
    }

    const quota = Number(d.quota_used || 0)
    bucket.cost[dayIdx] = Number((quota / QUOTA_PER_YUAN).toFixed(4))

    const hit = Number(d.cache_hit_tokens || 0)
    const write = Number(d.cache_write_tokens || 0)
    const input = Number(d.input_tokens ?? d.prompt_tokens ?? 0)
    const out = Number(d.completion_tokens || 0)
    const miss = Math.max(0, input - hit - write)
    const totalTok = hit + write + miss + out
    bucket.tokens[dayIdx] = totalTok > 0 ? totalTok : null

    const inputTok = hit + write + miss
    bucket.hitRate[dayIdx] =
      inputTok > 0 ? Number(((hit / inputTok) * 100).toFixed(1)) : null
  }

  // 取最近 4 个周(按周一时间戳降序),再翻回从旧到新
  const weeks = [...byWeek.values()].sort((a, b) => b.key - a.key).slice(0, 4).reverse()

  const hasToken = weeks.some(w => w.tokens.some(v => v !== null && v > 0))

  return { weeks, hasToken }
}

export function WeeklyPatternAnalytics({ dailyTrends, loading }: WeeklyPatternAnalyticsProps) {
  const model = useMemo(() => buildModel(dailyTrends), [dailyTrends])
  const option = useMemo<EChartsOption>(() => buildOption(model), [model])
  const hasData = model.weeks.length > 0

  return (
    <Card className="shadow-sm hover:shadow-lg transition-all duration-300 border-border/50">
      <CardHeader className="pb-2">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <CardTitle className="text-lg flex items-center gap-2">
            <div className="p-2 bg-primary/10 rounded-lg text-primary">
              <CalendarRange className="w-5 h-5" />
            </div>
            周内规律分析（近4周 · 按星期几）
          </CardTitle>
          <div className="text-xs text-muted-foreground self-center">
            花费 · Token · 缓存命中率
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {loading ? (
          <div className="h-[560px] animate-pulse bg-muted/20 rounded-lg" />
        ) : hasData ? (
          <ReactECharts
            option={option}
            style={{ height: 560, width: '100%' }}
            opts={{ renderer: 'canvas' }}
            notMerge
          />
        ) : (
          <div className="h-[560px] flex flex-col items-center justify-center text-muted-foreground bg-muted/5 rounded-xl border border-dashed border-muted">
            <BarChart3 className="w-10 h-10 mb-2 opacity-20" />
            <p className="text-sm">近4周暂无数据</p>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function buildOption(m: ChartModel): EChartsOption {
  const axisLine = { lineStyle: { color: 'rgba(120,120,120,0.3)' } }
  const splitLine = { lineStyle: { color: 'rgba(120,120,120,0.12)' } }
  const labelColor = 'rgba(130,130,130,0.95)'

  const legendData = m.weeks.map(w => weekRangeLabel(w.key))

  // 每个子图的柱状系列:按星期几分组,组内 4 根柱=最近 4 周。
  const makeBarSeries = (
    gridIndex: number,
    pick: (w: WeekBucket) => (number | null)[],
  ) =>
    m.weeks.map((w, i) => {
      const name = weekRangeLabel(w.key)
      const color = WEEK_COLORS[WEEK_COLORS.length - m.weeks.length + i] ?? WEEK_COLORS[i]
      return {
        name,
        type: 'bar' as const,
        xAxisIndex: gridIndex,
        yAxisIndex: gridIndex,
        data: pick(w),
        barMaxWidth: 14,
        barGap: '18%',
        barCategoryGap: '32%',
        itemStyle: { color, borderRadius: [2, 2, 0, 0] as [number, number, number, number] },
        emphasis: { focus: 'series' as const },
      }
    })

  const dot = (c: string) =>
    `<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${c}"></span>`

  return {
    animationDuration: 400,
    grid: [
      { left: 60, right: 24, top: 40, height: 118 },
      { left: 60, right: 24, top: 216, height: 118 },
      { left: 60, right: 24, top: 392, height: 118 },
    ],
    axisPointer: { link: [{ xAxisIndex: 'all' }], lineStyle: { color: 'rgba(120,120,120,0.45)' } },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(255,255,255,0.96)',
      borderColor: 'rgba(148,163,184,0.28)',
      borderWidth: 1,
      padding: [10, 12],
      textStyle: { color: '#475569', fontSize: 12 },
      confine: true,
      transitionDuration: 0.12,
      extraCssText: 'border-radius:8px;box-shadow:0 14px 36px rgba(15,23,42,0.11);backdrop-filter:blur(10px);',
      formatter: (params: any) => {
        const arr = Array.isArray(params) ? params : [params]
        if (!arr.length) return ''
        const dayIdx = arr[0].dataIndex
        const day = WEEK_LABELS[dayIdx] ?? ''
        const total = m.weeks.length
        // 列定义:dot | date(右对齐) | context | cost | delta | token | rate
        // 之前把 date+context 塞进 118px 单格,「本周」比「3周前」窄 1 字,
        // 视觉上后面 4 列的锚点跟着漂移。拆成两列后所有行严格对齐,
        // 日期右对齐配合 tabular-nums,数字风格也一致。
        const cols = '9px 38px 52px 60px 50px 60px 48px'
        const headerRow = (
          // header 颜色从 #94a3b8(slate-400)提到 #64748b(slate-500),
          // 在白底上更清晰可读,但仍弱于加粗的数据值(#334155),
          // 形成「表头 < 数据值」的层级关系。
          `<div style="display:grid;grid-template-columns:${cols};column-gap:8px;font-size:10px;color:#64748b;font-weight:500;margin-bottom:2px;align-items:center">` +
          `<span></span><span></span><span></span>` +
          // 4 个数值列统一居中:cost/环比/Token/命中率字符数都在 4-8 之间,
          // 居中对齐后整列视觉对称,不再有列头右对齐、列内右对齐的错位感
          `<span style="text-align:center">花费</span>` +
          `<span style="text-align:center">环比</span>` +
          `<span style="text-align:center">Token</span>` +
          `<span style="text-align:center">命中率</span></div>`
        )
        // 每个星期几,列出 4 周的 花费/Token/命中率
        // 每行用「具体日期 MM-DD」+「相对周次」取代原本的范围标签,
        // 避免用户看到「06-22 ~ 06-28」还要心算今天是周四的哪一天。
        const rows = m.weeks
          .map((w, i) => {
            const color = WEEK_COLORS[WEEK_COLORS.length - total + i] ?? WEEK_COLORS[i]
            const dateLabel = specificDateLabel(w.key, dayIdx)
            const weekCtx = relativeWeekLabel(i, total)
            const cost = w.cost[dayIdx]
            const tok = w.tokens[dayIdx]
            const rate = w.hitRate[dayIdx]
            if (cost === null && tok === null && rate === null) return ''
            const costStr = cost === null ? '—' : `¥${cost.toFixed(2)}`
            const tokStr = tok === null ? '—' : formatTokens(tok)
            const rateStr = rate === null ? '—' : `${rate.toFixed(1)}%`
            // 花费环比:较前一周(i-1)同一星期几的涨跌
            const prevCost = i > 0 ? m.weeks[i - 1].cost[dayIdx] : null
            const deltaStr = deltaLabel(prevCost, cost)
            return (
              `<div style="display:grid;grid-template-columns:${cols};column-gap:8px;align-items:center;line-height:22px">` +
              `${dot(color)}` +
              // 日期:右对齐 + tabular-nums,与右侧数字列风格一致
              `<span style="text-align:right;color:#334155;font-weight:600;font-variant-numeric:tabular-nums">${dateLabel}</span>` +
              // 相对周次:浅灰小号,左对齐
              `<span style="color:#94a3b8;font-size:10px">${weekCtx}</span>` +
              // 4 个数值列统一居中
              `<span style="text-align:center;color:#334155;font-weight:600;font-variant-numeric:tabular-nums">${costStr}</span>` +
              `<span style="text-align:center;font-variant-numeric:tabular-nums">${deltaStr}</span>` +
              `<span style="text-align:center;color:#334155;font-variant-numeric:tabular-nums">${tokStr}</span>` +
              `<span style="text-align:center;color:#10b981;font-variant-numeric:tabular-nums">${rateStr}</span></div>`
            )
          })
          .join('')
        return (
          `<div style="margin-bottom:6px"><strong style="font-size:13px;color:#334155">${day}</strong></div>` +
          headerRow +
          rows
        )
      },
    },
    legend: {
      data: legendData,
      top: 6,
      left: 'center',
      itemWidth: 18,
      itemHeight: 8,
      itemGap: 18,
      textStyle: { color: labelColor, fontSize: 11 },
    },
    // 三个子图各一个标题(ECharts graphic 简化为 title 数组)
    title: [
      { text: '额度花费 (¥)', left: 60, top: 24, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
      { text: 'Token 总量', left: 60, top: 200, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
      { text: '缓存命中率 (%)', left: 60, top: 376, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
    ],
    xAxis: [0, 1, 2].map(gridIndex => ({
      type: 'category' as const,
      gridIndex,
      data: WEEK_LABELS,
      boundaryGap: true,
      axisLine,
      axisTick: { show: false },
      axisLabel: { color: labelColor, fontSize: 10, show: gridIndex === 2 },
    })),
    yAxis: [
      {
        type: 'value' as const,
        gridIndex: 0,
        axisLabel: { color: labelColor, fontSize: 10, formatter: (v: number) => `¥${v}` },
        splitLine,
      },
      {
        type: 'value' as const,
        gridIndex: 1,
        axisLabel: { color: labelColor, fontSize: 10, formatter: (v: number) => formatTokens(v) },
        splitLine,
      },
      {
        type: 'value' as const,
        gridIndex: 2,
        min: 0,
        max: 100,
        axisLabel: { color: labelColor, fontSize: 10, formatter: '{value}%' },
        splitLine,
      },
    ],
    series: [
      ...makeBarSeries(0, w => w.cost),
      ...makeBarSeries(1, w => w.tokens),
      ...makeBarSeries(2, w => w.hitRate),
    ],
  }
}
