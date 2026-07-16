/**
 * SpendAnalytics —— 「花费分析（当天 · 按小时）」
 *
 * 一个卡片,上下两个图共用「小时」横坐标(ECharts 双 grid,x 轴 + tooltip 联动):
 * - 上图:每小时总花费折线(¥)
 * - 下图:每小时 Token 组成堆叠柱(缓存命中 / 缓存未命中 / 输出)
 *
 * 数据:dailyTrends —— 当天每小时数据,来自 /api/dashboard/trends/hourly。
 * 每个点字段:hour|timestamp、quota_used、prompt_tokens、completion_tokens、cache_hit_tokens。
 */

import { useMemo } from 'react'
import ReactECharts from 'echarts-for-react'
import type { EChartsOption } from 'echarts'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Wallet, BarChart3 } from 'lucide-react'
import { formatCost, formatTokens, QUOTA_PER_YUAN } from '../lib/format'

interface DailyTrend {
  date?: string
  hour?: string
  timestamp?: number
  request_count: number
  quota_used: number
  unique_users?: number
  prompt_tokens?: number
  completion_tokens?: number
  cache_hit_tokens?: number
  cache_write_tokens?: number
}

export interface SpendAnalyticsProps {
  dailyTrends: DailyTrend[]
  loading?: boolean
}

const COLOR_COST = '#0ea5e9' // sky —— 花费折线
const COLOR_HIT = '#22c55e' // green —— 缓存命中
const COLOR_MISS = '#6366f1' // indigo —— 缓存未命中
const COLOR_OUT = '#f59e0b' // amber —— 输出

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

interface ChartModel {
  cats: string[]
  cost: number[] // ¥
  hit: number[]
  miss: number[]
  out: number[]
  totalCost: number // raw quota
  totalReq: number
  totalTok: number
}

export function SpendAnalytics({ dailyTrends, loading }: SpendAnalyticsProps) {
  const model = useMemo<ChartModel>(() => {
    const cats: string[] = []
    const cost: number[] = []
    const hit: number[] = []
    const miss: number[] = []
    const out: number[] = []
    let totalCost = 0
    let totalReq = 0
    for (const d of dailyTrends) {
      cats.push(hourLabel(d))
      const quota = Number(d.quota_used || 0)
      totalCost += quota
      totalReq += Number(d.request_count || 0)
      cost.push(Number((quota / QUOTA_PER_YUAN).toFixed(4)))
      const h = Number(d.cache_hit_tokens || 0)
      const m = Math.max(0, Number(d.prompt_tokens || 0) - h)
      const o = Number(d.completion_tokens || 0)
      hit.push(h)
      miss.push(m)
      out.push(o)
    }
    const totalTok =
      hit.reduce((s, v) => s + v, 0) + miss.reduce((s, v) => s + v, 0) + out.reduce((s, v) => s + v, 0)
    return { cats, cost, hit, miss, out, totalCost, totalReq, totalTok }
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
            <div className="text-2xl font-bold text-primary tabular-nums">{formatCost(model.totalCost)}</div>
            <div className="text-xs text-muted-foreground tabular-nums">
              {model.totalReq.toLocaleString()} 请求 · {formatTokens(model.totalTok)} Token
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
    `<span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${c};margin-right:6px"></span>`

  return {
    animationDuration: 400,
    grid: [
      { left: 72, right: 24, top: 32, height: 162 },
      { left: 72, right: 24, top: 262, height: 162 },
    ],
    axisPointer: {
      link: [{ xAxisIndex: 'all' }],
      lineStyle: { color: 'rgba(120,120,120,0.45)' },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      formatter: (params: any) => {
        const arr = Array.isArray(params) ? params : [params]
        if (!arr.length) return ''
        const idx = arr[0].dataIndex
        const hour = m.cats[idx] ?? ''
        const cost = m.cost[idx] ?? 0
        const hit = m.hit[idx] ?? 0
        const miss = m.miss[idx] ?? 0
        const out = m.out[idx] ?? 0
        const tot = hit + miss + out
        const pct = (v: number) => (tot > 0 ? ((v / tot) * 100).toFixed(1) : '0.0')
        return [
          `<div style="font-weight:600;margin-bottom:4px">${hour}</div>`,
          `<div>${dot(COLOR_COST)}花费 <b>¥${cost.toFixed(2)}</b></div>`,
          `<div style="margin-top:4px;border-top:1px solid rgba(120,120,120,0.2);padding-top:4px">`,
          `<div>${dot(COLOR_HIT)}缓存命中 ${formatTokens(hit)} (${pct(hit)}%)</div>`,
          `<div>${dot(COLOR_MISS)}缓存未命中 ${formatTokens(miss)} (${pct(miss)}%)</div>`,
          `<div>${dot(COLOR_OUT)}输出 ${formatTokens(out)} (${pct(out)}%)</div>`,
          `<div style="margin-top:2px;color:#888">合计 ${formatTokens(tot)} Token</div>`,
          `</div>`,
        ].join('')
      },
    },
    legend: [
      {
        data: ['花费'],
        left: 72,
        top: 2,
        itemWidth: 14,
        itemHeight: 8,
        textStyle: { color: labelColor, fontSize: 10 },
      },
      {
        data: ['缓存命中', '缓存未命中', '输出'],
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
        name: '花费 (¥)',
        nameLocation: 'middle',
        nameRotate: 90,
        nameGap: 52,
        nameTextStyle: { color: labelColor, fontSize: 10 },
        axisLabel: { color: labelColor, fontSize: 10, formatter: (v: number) => `¥${v}` },
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
        splitLine,
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
          position: 'top',
          color: COLOR_COST,
          fontSize: 9,
          fontWeight: 'bold',
          formatter: (p: any) => `¥${Number(p.value).toFixed(2)}`,
        },
        labelLayout: { hideOverlap: true },
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
        name: '缓存命中', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.hit,
        itemStyle: { color: COLOR_HIT },
        label: { show: true, position: 'inside', color: '#fff', fontSize: 9, formatter: (p: any) => (Number(p.value) > 0 ? formatTokens(Number(p.value)) : '') },
        labelLayout: { hideOverlap: true },
      },
      {
        name: '缓存未命中', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.miss,
        itemStyle: { color: COLOR_MISS },
        label: { show: true, position: 'inside', color: '#fff', fontSize: 9, formatter: (p: any) => (Number(p.value) > 0 ? formatTokens(Number(p.value)) : '') },
        labelLayout: { hideOverlap: true },
      },
      {
        name: '输出', type: 'bar', stack: 'tok', xAxisIndex: 1, yAxisIndex: 1, data: m.out,
        itemStyle: { color: COLOR_OUT },
        label: { show: true, position: 'inside', color: '#fff', fontSize: 9, formatter: (p: any) => (Number(p.value) > 0 ? formatTokens(Number(p.value)) : '') },
        labelLayout: { hideOverlap: true },
      },
    ],
  }
}
