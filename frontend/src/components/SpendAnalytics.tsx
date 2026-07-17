/**
 * SpendAnalytics —— 「花费分析（当天 · 按小时）」
 *
 * 一个卡片,上下两个图共用「小时」横坐标(ECharts 双 grid,x 轴 + tooltip 联动):
 * - 上图:每小时总花费折线(¥,左轴) + 每小时缓存命中率折线(%,右轴,虚线)
 * - 下图:每小时 Token 组成堆叠柱(缓存命中 / 缓存写入 / 缓存未命中 / 输出)
 *
 * 数据:dailyTrends —— 当天完整小时缓存与当前小时实时数据的合并结果。
 * 每个点字段:hour|timestamp、quota_used、input_tokens、completion_tokens、cache_hit_tokens、cache_write_tokens。
 */

import { useMemo } from 'react'
import ReactECharts from 'echarts-for-react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Wallet, BarChart3 } from 'lucide-react'
import { formatCostPrecise, formatTokens, QUOTA_PER_YUAN } from '../lib/format'

interface DailyTrend {
  date?: string
  hour?: string
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

export interface SpendAnalyticsProps {
  dailyTrends: DailyTrend[]
  loading?: boolean
}

const COLOR_COST = '#0ea5e9' // sky —— 花费折线
const COLOR_HIT_RATE = '#10b981' // emerald —— 缓存命中率折线（右轴）
const COLOR_HIT = '#6ee7b7' // soft green —— 缓存命中
const COLOR_WRITE = '#f9a8d4' // soft pink —— 缓存写入
const COLOR_MISS = '#a5b4fc' // soft indigo —— 缓存未命中
const COLOR_OUT = '#fcd34d' // soft amber —— 输出
const TOKEN_AXIS_HEADROOM = 1.1
const TOKEN_BAR_MAX_WIDTH = 56

/** 取柱子/折线点的小时标签(HH:00)。 */
function hourLabel(d: DailyTrend): string {
  if (d.timestamp) {
    const dt = new Date(d.timestamp * 1000)
    return `${String(dt.getHours()).padStart(2, '0')}:00`
  }
  if (d.hour) {
    const t = d.hour.split(' ')[1]
    return t ? t.slice(0, 5) : d.hour.slice(-5)
  }
  return ''
}

function formatSignificant(n: number, digits = 3): string {
  if (!Number.isFinite(n) || n === 0) return '0'
  const decimals = Math.max(0, digits - 1 - Math.floor(Math.log10(Math.abs(n))))
  return n.toFixed(decimals)
}

function formatTooltipTokens(n: number): string {
  if (!Number.isFinite(n)) return '0'
  if (Math.abs(n) >= 1_000_000) return `${formatSignificant(n / 1_000_000)}M`
  if (Math.abs(n) >= 1_000) return `${formatSignificant(n / 1_000)}k`
  return Math.round(n).toLocaleString('zh-CN')
}

interface ChartModel {
  cats: string[]
  cost: number[] // ¥
  hit: number[]
  write: number[]
  miss: number[]
  out: number[]
  hitRate: (number | null)[] // 每小时缓存命中率（%），该小时无输入时为 null（折线断开）
  totalCost: number // raw quota
  totalReq: number
  totalTok: number
  cacheHitRate: number | null // 缓存命中 / 输入 Token（%），无输入时为 null
}

export function SpendAnalytics({ dailyTrends, loading }: SpendAnalyticsProps) {
  const model = useMemo<ChartModel>(() => {
    const cats: string[] = []
    const cost: number[] = []
    const hit: number[] = []
    const write: number[] = []
    const miss: number[] = []
    const out: number[] = []
    const hitRate: (number | null)[] = []
    let totalCost = 0
    let totalReq = 0
    for (const d of dailyTrends) {
      cats.push(hourLabel(d))
      const quota = Number(d.quota_used || 0)
      totalCost += quota
      totalReq += Number(d.request_count || 0)
      cost.push(Number((quota / QUOTA_PER_YUAN).toFixed(4)))
      const h = Number(d.cache_hit_tokens || 0)
      const w = Number(d.cache_write_tokens || 0)
      const input = Number(d.input_tokens ?? d.prompt_tokens ?? 0)
      const m = Math.max(0, input - h - w)
      const o = Number(d.completion_tokens || 0)
      hit.push(h)
      write.push(w)
      miss.push(m)
      out.push(o)
      const hourInput = h + w + m
      hitRate.push(hourInput > 0 ? Number(((h / hourInput) * 100).toFixed(1)) : null)
    }
    const totalTok =
      hit.reduce((s, v) => s + v, 0) + write.reduce((s, v) => s + v, 0) +
      miss.reduce((s, v) => s + v, 0) + out.reduce((s, v) => s + v, 0)
    const totalHit = hit.reduce((s, v) => s + v, 0)
    const totalInput =
      totalHit + write.reduce((s, v) => s + v, 0) + miss.reduce((s, v) => s + v, 0)
    const cacheHitRate = totalInput > 0 ? (totalHit / totalInput) * 100 : null
    return { cats, cost, hit, write, miss, out, hitRate, totalCost, totalReq, totalTok, cacheHitRate }
  }, [dailyTrends])

  const option = useMemo<EChartsOption>(() => buildOption(model), [model])
  const hasData = dailyTrends.length > 0

  return (
    <Card className="shadow-sm hover:shadow-lg transition-all duration-300 border-border/50">
      <CardHeader className="pb-2">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="text-lg flex items-center gap-2">
              <div className="p-2 bg-primary/10 rounded-lg text-primary">
                <Wallet className="w-5 h-5" />
              </div>
              花费分析（当天 · 按小时）
            </CardTitle>
          </div>
          <div className="text-right">
            <div className="text-2xl font-bold text-primary tabular-nums">{formatCostPrecise(model.totalCost)}</div>
            <div className="text-xs text-muted-foreground tabular-nums">
              {model.totalReq.toLocaleString()} 请求 · {formatTokens(model.totalTok)} Token
              {model.cacheHitRate !== null && (
                <>
                  {' · 缓存命中 '}
                  <span className="text-emerald-600 dark:text-emerald-400 font-medium">
                    {model.cacheHitRate.toFixed(1)}%
                  </span>
                </>
              )}
            </div>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {loading ? (
          <div className="h-[480px] animate-pulse bg-muted/20 rounded-lg" />
        ) : hasData ? (
          <ReactECharts
            option={option}
            style={{ height: 480, width: '100%' }}
            opts={{ renderer: 'canvas' }}
            notMerge
          />
        ) : (
          <div className="h-[480px] flex flex-col items-center justify-center text-muted-foreground bg-muted/5 rounded-xl border border-dashed border-muted">
            <BarChart3 className="w-10 h-10 mb-2 opacity-20" />
            <p className="text-sm">当天暂无数据</p>
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

  const dot = (c: string) =>
    `<span style="display:inline-block;width:7px;height:7px;border-radius:50%;background:${c}"></span>`

  const tooltipRow = (color: string, label: string, value: string, ratio = '') =>
    `<div style="display:grid;grid-template-columns:7px 64px 66px 48px;column-gap:7px;align-items:center;line-height:22px">` +
    `${dot(color)}<span style="color:#64748b">${label}</span>` +
    `<span style="text-align:right;color:#334155;font-weight:600;font-variant-numeric:tabular-nums">${value}</span>` +
    `<span style="text-align:right;color:#94a3b8;font-size:11px;font-variant-numeric:tabular-nums">${ratio}</span></div>`

  return {
    animationDuration: 400,
    grid: [
      // 上图 top 从 32 → 40,给贴顶的命中率标签留出垂直空间,避免被裁切或压到 legend
      { left: 72, right: 56, top: 40, height: 162 },
      { left: 72, right: 56, top: 262, height: 162 },
    ],
    axisPointer: {
      link: [{ xAxisIndex: 'all' }],
      lineStyle: { color: 'rgba(120,120,120,0.45)' },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      backgroundColor: 'rgba(255,255,255,0.96)',
      borderColor: 'rgba(148,163,184,0.28)',
      borderWidth: 1,
      padding: [10, 12],
      textStyle: { color: '#475569', fontSize: 12 },
      confine: true,
      transitionDuration: 0.12,
      extraCssText: 'border-radius:8px;box-shadow:0 14px 36px rgba(15,23,42,0.11);backdrop-filter:blur(10px);font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;',
      formatter: (params: any) => {
        const arr = Array.isArray(params) ? params : [params]
        if (!arr.length) return ''
        const idx = arr[0].dataIndex
        const hour = m.cats[idx] ?? ''
        const cost = m.cost[idx] ?? 0
        const hit = m.hit[idx] ?? 0
        const write = m.write[idx] ?? 0
        const miss = m.miss[idx] ?? 0
        const out = m.out[idx] ?? 0
        const tot = hit + write + miss + out
        const rate = m.hitRate[idx]
        const pct = (v: number) => (tot > 0 ? formatSignificant((v / tot) * 100) : '0')
        return [
          `<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;margin-bottom:7px">` +
          `<strong style="font-size:13px;color:#334155">${hour}</strong>` +
          `<span style="display:flex;align-items:baseline;gap:6px"><span style="color:#94a3b8;font-size:11px">花费</span>` +
          `<strong style="color:${COLOR_COST};font-variant-numeric:tabular-nums">¥${cost.toFixed(2)}</strong></span></div>`,
          rate !== null && rate !== undefined
            ? `<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;margin-bottom:7px">` +
              `<span style="display:flex;align-items:center;gap:6px">${dot(COLOR_HIT_RATE)}` +
              `<span style="color:#94a3b8;font-size:11px">缓存命中率</span></span>` +
              `<strong style="color:${COLOR_HIT_RATE};font-variant-numeric:tabular-nums">${rate.toFixed(1)}%</strong></div>`
            : '',
          `<div style="border-top:1px solid rgba(148,163,184,0.20);padding-top:5px">`,
          tooltipRow(COLOR_HIT, '缓存命中', formatTooltipTokens(hit), `${pct(hit)}%`),
          tooltipRow(COLOR_WRITE, '缓存写入', formatTooltipTokens(write), `${pct(write)}%`),
          tooltipRow(COLOR_MISS, '缓存未命中', formatTooltipTokens(miss), `${pct(miss)}%`),
          tooltipRow(COLOR_OUT, '输出', formatTooltipTokens(out), `${pct(out)}%`),
          `<div style="display:flex;align-items:center;justify-content:space-between;margin-top:5px;padding-top:7px;border-top:1px solid rgba(148,163,184,0.20);font-variant-numeric:tabular-nums">` +
          `<span style="color:#64748b">总 Token</span>` +
          `<strong style="color:#0f172a;font-size:13px">${formatTooltipTokens(tot)}</strong></div>`,
          `</div>`,
        ].join('')
      },
    },
    legend: [
      {
        data: ['花费', '缓存命中率'],
        left: 72,
        top: 2,
        itemWidth: 14,
        itemHeight: 8,
        textStyle: { color: labelColor, fontSize: 10 },
      },
      {
        data: ['缓存命中', '缓存写入', '缓存未命中', '输出'],
        left: 72,
        top: 232,
        itemWidth: 12,
        itemHeight: 8,
        itemGap: 16,
        textStyle: { color: labelColor, fontSize: 10 },
      },
    ],
    xAxis: [
      {
        type: 'category',
        gridIndex: 0,
        data: m.cats,
        boundaryGap: true,
        axisLine,
        axisTick: { show: false, alignWithLabel: true },
        axisLabel: { color: labelColor, fontSize: 10 },
      },
      {
        type: 'category',
        gridIndex: 1,
        data: m.cats,
        boundaryGap: true,
        axisLine,
        axisTick: { show: false, alignWithLabel: true },
        axisLabel: { color: labelColor, fontSize: 10 },
      },
    ],
    yAxis: [
      {
        type: 'value',
        gridIndex: 0,
        // 刻度已带 ¥ 前缀，不再重复轴标题；tooltip 已有精确值，隐藏指针数值标签
        axisLabel: { color: labelColor, fontSize: 10, formatter: (v: number) => `¥${v}` },
        axisPointer: { label: { show: false } },
        splitLine,
      },
      {
        type: 'value',
        gridIndex: 1,
        name: 'Token',
        nameLocation: 'middle',
        nameRotate: 90,
        nameGap: 52,
        nameTextStyle: { color: labelColor, fontSize: 10 },
        axisLabel: { color: labelColor, fontSize: 10, formatter: (v: number) => formatTokens(v) },
        axisPointer: { label: { show: false } },
        max: ({ max }: { max: number }) => (max > 0 ? max * TOKEN_AXIS_HEADROOM : 1),
        splitLine,
      },
      {
        // 右轴：缓存命中率（%），固定 0-100，不画网格线避免与花费轴网格混淆
        type: 'value',
        gridIndex: 0,
        position: 'right',
        min: 0,
        max: 100,
        axisLabel: { color: COLOR_HIT_RATE, fontSize: 10, formatter: '{value}%' },
        axisPointer: { label: { show: false } },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: '花费',
        type: 'line',
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: m.cost,
        smooth: true,
        showSymbol: true,
        symbol: 'circle',
        symbolSize: 5,
        lineStyle: { width: 2, color: COLOR_COST },
        itemStyle: { color: COLOR_COST },
        label: {
          show: true,
          // 花费线在 09:00-16:00 长时间保持高位(¥2200~3050),标签若贴在线上方
          // (position: 'top' distance 3)会跟同一水平高度的相邻标签挤成一团。
          // 改为放到线下方(distance 8,落入 area 填充区),配白底内边距保证可读;
          // 这样与命中率标签(贴顶)自然分层,且不再有同高度多标签相撞的问题。
          position: 'bottom',
          distance: 8,
          color: COLOR_COST,
          fontSize: 8,
          fontWeight: 'bold',
          backgroundColor: 'rgba(255,255,255,0.92)',
          borderColor: 'rgba(14,165,233,0.25)',
          borderWidth: 0.5,
          borderRadius: 2,
          padding: [1, 3],
          // 低花费时段(<¥100)放线下会落到图表外,直接隐藏避免裁切
          formatter: (p: any) => {
            const v = Number(p.value);
            if (!Number.isFinite(v) || v < 100) return '';
            return `¥${v.toFixed(2)}`;
          },
        },
        labelLayout: { hideOverlap: true, moveOverlap: 'shiftY' },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(14,165,233,0.35)' },
              { offset: 1, color: 'rgba(14,165,233,0.02)' },
            ],
          },
        },
      },
      {
        // 缓存命中率（右轴 %）：虚线区分于花费实线；无输入的小时为 null，折线断开
        name: '缓存命中率',
        type: 'line',
        xAxisIndex: 0,
        yAxisIndex: 2,
        data: m.hitRate,
        smooth: true,
        showSymbol: true,
        symbol: 'circle',
        symbolSize: 4,
        connectNulls: false,
        lineStyle: { width: 2, color: COLOR_HIT_RATE, type: 'dashed' },
        itemStyle: { color: COLOR_HIT_RATE },
        label: {
          show: true,
          // 命中率线常年在 85-96%,与峰值时的花费线纵向高度接近。
          // 把命中率标签放到线上方并加大 distance(6),让它贴到图表顶部,
          // 与花费标签（distance 3,靠近线）形成两层纵向分层,避免重叠。
          position: 'top',
          distance: 6,
          color: COLOR_HIT_RATE,
          fontSize: 8,
          fontWeight: 'bold',
          formatter: (p: any) =>
            p.value === null || p.value === undefined ? '' : `${Number(p.value).toFixed(1)}%`,
        },
        labelLayout: { hideOverlap: true, moveOverlap: 'shiftY' },
        z: 5,
      },
      {
        name: '缓存命中', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.hit,
        barMaxWidth: TOKEN_BAR_MAX_WIDTH,
        itemStyle: { color: COLOR_HIT },
        label: { show: false },
      },
      {
        name: '缓存写入', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.write,
        barMaxWidth: TOKEN_BAR_MAX_WIDTH,
        itemStyle: { color: COLOR_WRITE },
        label: { show: false },
      },
      {
        name: '缓存未命中', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.miss,
        barMaxWidth: TOKEN_BAR_MAX_WIDTH,
        itemStyle: { color: COLOR_MISS },
        label: { show: false },
      },
      {
        name: '输出', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1,
        data: m.out.map(value => (value > 0 ? value : null)),
        barMaxWidth: TOKEN_BAR_MAX_WIDTH,
        itemStyle: { color: COLOR_OUT, borderRadius: [3, 3, 0, 0] },
        label: { show: false },
      },
      {
        name: '总 Token',
        type: 'scatter',
        xAxisIndex: 1,
        yAxisIndex: 1,
        data: m.cats.map((_, index) =>
          (m.hit[index] || 0) + (m.write[index] || 0) + (m.miss[index] || 0) + (m.out[index] || 0)
        ),
        symbolSize: 1,
        itemStyle: { color: 'transparent' },
        silent: true,
        tooltip: { show: false },
        z: 10,
        label: {
          show: true,
          position: 'top',
          distance: 4,
          color: '#64748b',
          fontSize: 10,
          fontWeight: 600,
          formatter: (p: any) => Number(p.value) > 0 ? formatTooltipTokens(Number(p.value)) : '',
        },
      },
    ],
  }
}
