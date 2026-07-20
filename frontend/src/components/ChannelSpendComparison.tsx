import { useMemo } from 'react'
import type { EChartsOption } from 'echarts'
import { DashboardECharts } from './DashboardECharts'
import { BarChart3, CircleDollarSign, TrendingDown, TrendingUp } from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { cn } from '../lib/utils'
import { formatCostPrecise, QUOTA_PER_YUAN } from '../lib/format'

export type ChannelSpendDays = 7 | 30

interface ChannelCostPoint {
  date: string
  total_quota: number
  total_tokens: number
  total_requests: number
}

interface ChannelCostTrend {
  channel_id: number
  channel_name: string
  current: ChannelCostPoint[]
  previous?: ChannelCostPoint[]
}

export interface ChannelCostTrendData {
  channels: ChannelCostTrend[]
  compare_mode?: 'week_over_week' | 'month_over_month'
  compare_offset?: number
}

interface ChannelSpendComparisonProps {
  data: ChannelCostTrendData | null
  days: ChannelSpendDays
  loading?: boolean
  onDaysChange: (days: ChannelSpendDays) => void
}

interface ChannelSpendRow {
  id: number
  name: string
  currentQuota: number
  previousQuota: number
  requests: number
  share: number
  change: number | null
}

const MAX_VISIBLE_CHANNELS = 8
const COLOR_CURRENT = '#0ea5e9'
const COLOR_PREVIOUS = '#cbd5e1'

function sumPoints(points: ChannelCostPoint[] | undefined, key: 'total_quota' | 'total_requests'): number {
  return (points || []).reduce((sum, point) => sum + Number(point[key] || 0), 0)
}

function compactCost(quota: number): string {
  const value = quota / QUOTA_PER_YUAN
  if (Math.abs(value) >= 10_000) return `¥${(value / 10_000).toFixed(1)}万`
  if (Math.abs(value) >= 1_000) return `¥${(value / 1_000).toFixed(1)}k`
  return `¥${value.toFixed(2)}`
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>'"]/g, char => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    "'": '&#39;',
    '"': '&quot;',
  })[char] || char)
}

export function ChannelSpendComparison({ data, days, loading, onDaysChange }: ChannelSpendComparisonProps) {
  const model = useMemo(() => {
    const baseRows: ChannelSpendRow[] = (data?.channels || []).map(channel => {
      const currentQuota = sumPoints(channel.current, 'total_quota')
      const previousQuota = sumPoints(channel.previous, 'total_quota')
      return {
        id: Number(channel.channel_id),
        name: channel.channel_name || `Channel#${channel.channel_id}`,
        currentQuota,
        previousQuota,
        requests: sumPoints(channel.current, 'total_requests'),
        share: 0,
        change: previousQuota > 0 ? ((currentQuota - previousQuota) / previousQuota) * 100 : null,
      }
    })

    const totalCurrent = baseRows.reduce((sum, row) => sum + row.currentQuota, 0)
    const totalPrevious = baseRows.reduce((sum, row) => sum + row.previousQuota, 0)
    const rows = baseRows
      .map(row => ({ ...row, share: totalCurrent > 0 ? (row.currentQuota / totalCurrent) * 100 : 0 }))
      .sort((a, b) => b.currentQuota - a.currentQuota || b.previousQuota - a.previousQuota)
    const totalChange = totalPrevious > 0 ? ((totalCurrent - totalPrevious) / totalPrevious) * 100 : null

    return {
      rows,
      visibleRows: rows.slice(0, MAX_VISIBLE_CHANNELS),
      totalCurrent,
      totalPrevious,
      totalChange,
    }
  }, [data])

  const currentLabel = days === 7 ? '近 7 天' : '近 30 天'
  const previousLabel = days === 7 ? '前一周' : '前一月'
  const option = useMemo<EChartsOption>(() => buildOption(model.visibleRows, currentLabel, previousLabel), [model.visibleRows, currentLabel, previousLabel])
  const chartHeight = Math.max(330, model.visibleRows.length * 42 + 92)
  const hasData = model.rows.length > 0 && (model.totalCurrent > 0 || model.totalPrevious > 0)

  return (
    <Card className="shadow-sm hover:shadow-lg transition-all duration-300 border-border/50" data-testid="channel-spend-comparison">
      <CardHeader className="pb-2">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div className="space-y-1">
            <CardTitle className="text-lg flex items-center gap-2">
              <div className="p-2 bg-sky-500/10 rounded-lg text-sky-600 dark:text-sky-400">
                <CircleDollarSign className="w-5 h-5" />
              </div>
              渠道花费对比
            </CardTitle>
            <p className="text-xs text-muted-foreground">按已完成日统计，比较渠道占比与上一周期变化</p>
          </div>

          <div className="flex items-center justify-between gap-3 sm:justify-end">
            {!loading && hasData && (
              <div className="text-right">
                <div className="text-xl font-bold tabular-nums">{formatCostPrecise(model.totalCurrent)}</div>
                <ChangeLabel value={model.totalChange} prefix={`较${previousLabel}`} />
              </div>
            )}
            <div className="inline-flex h-9 rounded-md border bg-muted/30 p-1" role="group" aria-label="渠道花费统计周期">
              {([7, 30] as ChannelSpendDays[]).map(value => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={days === value}
                  onClick={() => onDaysChange(value)}
                  className={cn(
                    'min-w-[58px] rounded-sm px-3 text-xs font-medium transition-colors',
                    days === value
                      ? 'bg-background text-foreground shadow-sm'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  {value} 天
                </button>
              ))}
            </div>
          </div>
        </div>
      </CardHeader>

      <CardContent>
        {loading ? (
          <div className="grid min-h-[360px] gap-6 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,0.75fr)]">
            <div className="animate-pulse rounded-md bg-muted/20" />
            <div className="hidden animate-pulse rounded-md bg-muted/20 xl:block" />
          </div>
        ) : hasData ? (
          <div className="grid gap-6 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,0.75fr)]">
            <div className="min-w-0">
              <DashboardECharts
                option={option}
                style={{ height: chartHeight, width: '100%' }}
                opts={{ renderer: 'canvas' }}
                notMerge
              />
            </div>

            <div className="xl:border-l xl:pl-6">
              <div className="mb-3 flex items-center justify-between text-xs text-muted-foreground">
                <span>渠道排行</span>
                <span>花费占比 / 周期变化</span>
              </div>
              <div className="divide-y">
                {model.visibleRows.map((row, index) => (
                  <div key={row.id} className="grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-2 py-3">
                    <span className={cn(
                      'flex h-6 w-6 items-center justify-center rounded-sm text-[11px] font-semibold',
                      index < 3 ? 'bg-sky-500/10 text-sky-700 dark:text-sky-300' : 'bg-muted text-muted-foreground',
                    )}>
                      {index + 1}
                    </span>
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium" title={row.name}>{row.name}</div>
                      <div className="text-[11px] text-muted-foreground tabular-nums">
                        {row.requests.toLocaleString('zh-CN')} 请求 · {row.share.toFixed(1)}%
                      </div>
                    </div>
                    <div className="text-right">
                      <div className="text-sm font-semibold tabular-nums">{compactCost(row.currentQuota)}</div>
                      <ChangeLabel value={row.change} />
                    </div>
                  </div>
                ))}
              </div>
              {model.rows.length > MAX_VISIBLE_CHANNELS && (
                <p className="mt-3 text-right text-[11px] text-muted-foreground">
                  当前展示花费最高的 {MAX_VISIBLE_CHANNELS} 个渠道，共 {model.rows.length} 个
                </p>
              )}
            </div>
          </div>
        ) : (
          <div className="h-[360px] flex flex-col items-center justify-center rounded-md border border-dashed bg-muted/5 text-muted-foreground">
            <BarChart3 className="mb-2 h-10 w-10 opacity-20" />
            <p className="text-sm">该周期暂无渠道花费数据</p>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function ChangeLabel({ value, prefix }: { value: number | null; prefix?: string }) {
  if (value === null || !Number.isFinite(value)) {
    return <div className="text-[11px] text-muted-foreground">{prefix ? `${prefix}暂无基线` : '暂无基线'}</div>
  }

  const rising = value >= 0
  const Icon = rising ? TrendingUp : TrendingDown
  return (
    <div className={cn(
      'flex items-center justify-end gap-1 text-[11px] font-medium tabular-nums',
      rising ? 'text-rose-600 dark:text-rose-400' : 'text-emerald-600 dark:text-emerald-400',
    )}>
      <Icon className="h-3 w-3" />
      {prefix && <span className="text-muted-foreground font-normal">{prefix}</span>}
      <span>{rising ? '+' : ''}{value.toFixed(1)}%</span>
    </div>
  )
}

function buildOption(rows: ChannelSpendRow[], currentLabel: string, previousLabel: string): EChartsOption {
  return {
    animationDuration: 350,
    grid: { left: 12, right: 24, top: 52, bottom: 16, containLabel: true },
    legend: {
      data: [currentLabel, previousLabel],
      top: 4,
      left: 4,
      itemWidth: 12,
      itemHeight: 8,
      textStyle: { color: '#64748b', fontSize: 11 },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      confine: true,
      backgroundColor: 'rgba(255,255,255,0.97)',
      borderColor: 'rgba(148,163,184,0.28)',
      borderWidth: 1,
      padding: [10, 12],
      textStyle: { color: '#475569', fontSize: 12 },
      extraCssText: 'border-radius:8px;box-shadow:0 14px 36px rgba(15,23,42,0.11);font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;',
      formatter: (params: any) => {
        const list = Array.isArray(params) ? params : [params]
        const index = list[0]?.dataIndex ?? 0
        const row = rows[index]
        if (!row) return ''
        const change = row.change === null ? '暂无基线' : `${row.change >= 0 ? '+' : ''}${row.change.toFixed(1)}%`
        return [
          `<div style="font-weight:600;color:#334155;margin-bottom:7px">${escapeHtml(row.name)}</div>`,
          `<div style="display:grid;grid-template-columns:10px 64px auto;gap:7px;align-items:center;line-height:22px">`,
          `<span style="width:8px;height:8px;border-radius:2px;background:${COLOR_CURRENT}"></span><span>${currentLabel}</span><strong style="text-align:right;color:#0f172a">${formatCostPrecise(row.currentQuota)}</strong>`,
          `<span style="width:8px;height:8px;border-radius:2px;background:${COLOR_PREVIOUS}"></span><span>${previousLabel}</span><strong style="text-align:right;color:#64748b">${formatCostPrecise(row.previousQuota)}</strong>`,
          `</div><div style="display:flex;justify-content:space-between;gap:28px;margin-top:7px;padding-top:7px;border-top:1px solid rgba(148,163,184,.22)"><span style="color:#64748b">周期变化</span><strong>${change}</strong></div>`,
        ].join('')
      },
    },
    xAxis: {
      type: 'value',
      axisLabel: { color: '#94a3b8', fontSize: 10, formatter: (value: number) => compactCost(value * QUOTA_PER_YUAN) },
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: 'rgba(148,163,184,0.14)' } },
    },
    yAxis: {
      type: 'category',
      inverse: true,
      data: rows.map(row => row.name),
      axisLabel: {
        color: '#64748b',
        fontSize: 11,
        width: 132,
        overflow: 'truncate',
      },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [
      {
        name: currentLabel,
        type: 'bar',
        data: rows.map(row => Number((row.currentQuota / QUOTA_PER_YUAN).toFixed(4))),
        barMaxWidth: 14,
        itemStyle: { color: COLOR_CURRENT, borderRadius: [0, 3, 3, 0] },
      },
      {
        name: previousLabel,
        type: 'bar',
        data: rows.map(row => Number((row.previousQuota / QUOTA_PER_YUAN).toFixed(4))),
        barMaxWidth: 14,
        itemStyle: { color: COLOR_PREVIOUS, borderRadius: [0, 3, 3, 0] },
      },
    ],
  }
}
