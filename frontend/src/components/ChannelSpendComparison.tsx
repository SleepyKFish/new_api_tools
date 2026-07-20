import { useEffect, useMemo, useRef, useState } from 'react'
import type { EChartsOption } from 'echarts'
import { BarChart3, Check, ChevronDown, CircleDollarSign, Search, TrendingDown, TrendingUp } from 'lucide-react'
import { DashboardECharts } from './DashboardECharts'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Button } from './ui/button'
import { cn } from '../lib/utils'
import { formatCostPrecise, formatTokens, QUOTA_PER_YUAN } from '../lib/format'
import {
  mondayIndex,
  relativeWeekLabelFromDate,
  WEEK_COLORS,
  WEEKDAY_LABELS,
  weekRangeLabel,
  weekStartKey,
} from '../lib/week'

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
}

export interface ChannelCostTrendData {
  channels: ChannelCostTrend[]
}

interface ChannelSpendComparisonProps {
  data: ChannelCostTrendData | null
  loading?: boolean
}

interface ChannelWeekBucket {
  key: number
  cost: (number | null)[]
  tokens: (number | null)[]
  requests: (number | null)[]
}

interface ChannelPattern {
  id: number
  name: string
  weeks: ChannelWeekBucket[]
  totalQuota: number
  latestQuota: number
  change: number | null
}

function parseLocalDate(value: string): Date | null {
  const parts = value.split('-').map(Number)
  if (parts.length !== 3 || !parts.every(Number.isFinite)) return null
  return new Date(parts[0], parts[1] - 1, parts[2])
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

function percentageChange(previous: number, current: number): number | null {
  return previous > 0 ? ((current - previous) / previous) * 100 : null
}

function sumValues(values: (number | null)[]): number {
  return values.reduce<number>((sum, value) => sum + Number(value || 0), 0)
}

function addValue(values: (number | null)[], index: number, value: number) {
  values[index] = Number(values[index] || 0) + value
}

function specificDateLabel(mondayMs: number, dayIndex: number): string {
  const date = new Date(mondayMs)
  date.setDate(date.getDate() + dayIndex)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

function deltaHtml(previous: number | null, current: number | null): string {
  if (previous === null || current === null) return '<span style="color:#cbd5e1">—</span>'
  const change = percentageChange(previous, current)
  if (change === null) return '<span style="color:#cbd5e1">—</span>'
  if (Math.abs(change) < 0.05) return '<span style="color:#94a3b8">0%</span>'
  const rising = change > 0
  const color = rising ? '#ef4444' : '#10b981'
  return `<span style="color:${color}">${rising ? '↑' : '↓'}${Math.abs(change).toFixed(1)}%</span>`
}

export function ChannelSpendComparison({ data, loading }: ChannelSpendComparisonProps) {
  const [selectedChannelId, setSelectedChannelId] = useState<number | null>(null)
  const model = useMemo(() => buildModel(data), [data])
  const selectedChannel = model.channels.find(channel => channel.id === selectedChannelId) || model.channels[0] || null
  const option = useMemo<EChartsOption>(
    () => buildOption(selectedChannel),
    [selectedChannel],
  )
  const hasData = selectedChannel !== null && selectedChannel.totalQuota > 0

  return (
    <Card className="shadow-sm hover:shadow-lg transition-all duration-300 border-border/50" data-testid="channel-spend-comparison">
      <CardHeader className="pb-2">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="space-y-1">
            <CardTitle className="text-lg flex items-center gap-2">
              <div className="p-2 bg-sky-500/10 rounded-lg text-sky-600 dark:text-sky-400">
                <CircleDollarSign className="w-5 h-5" />
              </div>
              渠道周内规律（近4周）
            </CardTitle>
            <p className="text-xs text-muted-foreground">花费 · Token · 请求数</p>
          </div>

          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between lg:justify-end">
            {!loading && hasData && selectedChannel && (
              <div className="text-left sm:text-right">
                <div className="text-[11px] text-muted-foreground">今日花费</div>
                <div className="text-xl font-bold tabular-nums">{formatCostPrecise(selectedChannel.latestQuota)}</div>
                <ChangeLabel value={selectedChannel.change} prefix="较一周前" />
              </div>
            )}
            <ChannelPicker
              channels={model.channels}
              selectedId={selectedChannel?.id ?? null}
              onSelect={setSelectedChannelId}
            />
          </div>
        </div>
      </CardHeader>

      <CardContent>
        {loading ? (
          <div className="h-[560px] animate-pulse rounded-md bg-muted/20" />
        ) : hasData ? (
          <DashboardECharts
            option={option}
            style={{ height: 560, width: '100%' }}
            opts={{ renderer: 'canvas' }}
            notMerge
          />
        ) : (
          <div className="h-[560px] flex flex-col items-center justify-center rounded-md border border-dashed bg-muted/5 text-muted-foreground">
            <BarChart3 className="mb-2 h-10 w-10 opacity-20" />
            <p className="text-sm">该渠道近 4 周暂无数据</p>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function buildModel(data: ChannelCostTrendData | null): { channels: ChannelPattern[] } {
  const weekKeySet = new Set<number>()
  for (const channel of data?.channels || []) {
    for (const point of channel.current || []) {
      const date = parseLocalDate(point.date)
      if (date) weekKeySet.add(weekStartKey(date))
    }
  }

  const weekKeys = [...weekKeySet].sort((a, b) => a - b).slice(-4)
  const weekIndexes = new Map(weekKeys.map((key, index) => [key, index]))
  const channels = (data?.channels || []).map(channel => {
    const weeks: ChannelWeekBucket[] = weekKeys.map(key => ({
      key,
      cost: Array(7).fill(null),
      tokens: Array(7).fill(null),
      requests: Array(7).fill(null),
    }))

    for (const point of channel.current || []) {
      const date = parseLocalDate(point.date)
      if (!date) continue
      const weekIndex = weekIndexes.get(weekStartKey(date))
      if (weekIndex === undefined) continue
      const dayIndex = mondayIndex(date)
      addValue(weeks[weekIndex].cost, dayIndex, Number(point.total_quota || 0))
      addValue(weeks[weekIndex].tokens, dayIndex, Number(point.total_tokens || 0))
      addValue(weeks[weekIndex].requests, dayIndex, Number(point.total_requests || 0))
    }

    const latestWeek = weeks[weeks.length - 1]
    const previousWeek = weeks[weeks.length - 2]
    const latestDayIndex = latestWeek?.cost.reduce(
      (lastIndex, value, index) => value === null ? lastIndex : index,
      -1,
    ) ?? -1
    const latestQuota = latestDayIndex >= 0 ? Number(latestWeek?.cost[latestDayIndex] || 0) : 0
    const previousQuota = latestDayIndex >= 0 ? Number(previousWeek?.cost[latestDayIndex] || 0) : 0
    return {
      id: Number(channel.channel_id),
      name: channel.channel_name || `Channel#${channel.channel_id}`,
      weeks,
      totalQuota: weeks.reduce((sum, week) => sum + sumValues(week.cost), 0),
      latestQuota,
      change: percentageChange(previousQuota, latestQuota),
    }
  })

  channels.sort((a, b) => b.latestQuota - a.latestQuota || b.totalQuota - a.totalQuota)
  return { channels }
}

function ChannelPicker({
  channels,
  selectedId,
  onSelect,
}: {
  channels: ChannelPattern[]
  selectedId: number | null
  onSelect: (id: number) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const containerRef = useRef<HTMLDivElement>(null)
  const selected = channels.find(channel => channel.id === selectedId) || null
  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase()
    if (!keyword) return channels
    return channels.filter(channel =>
      channel.name.toLowerCase().includes(keyword) || String(channel.id).includes(keyword),
    )
  }, [channels, query])

  useEffect(() => {
    const handlePointerDown = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [])

  return (
    <div ref={containerRef} className="relative min-w-0 sm:w-[240px]">
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => {
          setOpen(value => !value)
          if (open) setQuery('')
        }}
        className="relative h-9 w-full justify-center px-8"
      >
        <span className="flex min-w-0 items-center justify-center gap-2 overflow-hidden">
          <span className="truncate">{selected?.name || '暂无渠道'}</span>
          <span className="shrink-0 text-[11px] font-normal text-muted-foreground">{channels.length}个</span>
        </span>
        <ChevronDown className={cn('absolute right-3 h-4 w-4 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </Button>

      {open && (
        <div className="absolute right-0 top-full z-30 mt-2 w-[min(360px,calc(100vw-3rem))] rounded-md border bg-popover p-2 text-popover-foreground shadow-xl">
          <div className="relative mb-2">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <input
              value={query}
              onChange={event => setQuery(event.target.value)}
              placeholder="搜索渠道名称或 ID"
              autoFocus
              className="h-9 w-full rounded-md border bg-background pl-8 pr-3 text-sm outline-none focus:ring-2 focus:ring-ring"
            />
          </div>
          <div className="grid grid-cols-[minmax(0,1fr)_84px_16px] gap-2 px-2.5 pb-1 text-[10px] text-muted-foreground">
            <span>渠道</span>
            <span className="text-right">今日 / 较一周前</span>
            <span />
          </div>
          <div className="max-h-64 overflow-y-auto" role="listbox" aria-label="渠道列表">
            {filtered.map(channel => {
              const active = channel.id === selectedId
              return (
                <button
                  key={channel.id}
                  type="button"
                  role="option"
                  aria-selected={active}
                  onClick={() => {
                    onSelect(channel.id)
                    setOpen(false)
                    setQuery('')
                  }}
                  className={cn(
                    'grid w-full grid-cols-[minmax(0,1fr)_84px_16px] items-center gap-2 rounded px-2.5 py-2 text-left text-sm hover:bg-muted',
                    active && 'bg-sky-500/10 text-sky-700 dark:text-sky-300',
                  )}
                >
                  <span className="min-w-0">
                    <span className="block truncate font-medium">{channel.name}</span>
                    <span className="block text-[10px] text-muted-foreground">ID {channel.id}</span>
                  </span>
                  <span className="text-right">
                    <span className="block text-xs font-semibold tabular-nums text-foreground">{compactCost(channel.latestQuota)}</span>
                    <CompactChange value={channel.change} />
                  </span>
                  {active ? <Check className="h-4 w-4" /> : <span />}
                </button>
              )
            })}
            {filtered.length === 0 && (
              <div className="px-3 py-8 text-center text-sm text-muted-foreground">未找到匹配渠道</div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function CompactChange({ value }: { value: number | null }) {
  if (value === null || !Number.isFinite(value)) {
    return <span className="block text-[10px] text-muted-foreground">暂无基线</span>
  }

  const rising = value >= 0
  const Icon = rising ? TrendingUp : TrendingDown
  return (
    <span className={cn(
      'flex items-center justify-end gap-0.5 text-[10px] font-medium tabular-nums',
      rising ? 'text-rose-600 dark:text-rose-400' : 'text-emerald-600 dark:text-emerald-400',
    )}>
      <Icon className="h-2.5 w-2.5" />
      {rising ? '+' : ''}{value.toFixed(1)}%
    </span>
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
      'flex items-center gap-1 text-[11px] font-medium tabular-nums sm:justify-end',
      rising ? 'text-rose-600 dark:text-rose-400' : 'text-emerald-600 dark:text-emerald-400',
    )}>
      <Icon className="h-3 w-3" />
      {prefix && <span className="text-muted-foreground font-normal">{prefix}</span>}
      <span>{rising ? '+' : ''}{value.toFixed(1)}%</span>
    </div>
  )
}

function buildOption(channel: ChannelPattern | null): EChartsOption {
  const weeks = channel?.weeks || []
  const legendData = weeks.map(week => weekRangeLabel(week.key))
  const axisLine = { lineStyle: { color: 'rgba(120,120,120,0.3)' } }
  const splitLine = { lineStyle: { color: 'rgba(120,120,120,0.12)' } }
  const labelColor = 'rgba(130,130,130,0.95)'
  const colorForWeek = (index: number) => WEEK_COLORS[WEEK_COLORS.length - weeks.length + index] ?? WEEK_COLORS[index]

  const makeBarSeries = (
    gridIndex: number,
    pick: (week: ChannelWeekBucket) => (number | null)[],
  ) => weeks.map((week, index) => ({
    name: weekRangeLabel(week.key),
    type: 'bar' as const,
    xAxisIndex: gridIndex,
    yAxisIndex: gridIndex,
    data: pick(week),
    barMaxWidth: 14,
    barGap: '18%',
    barCategoryGap: '32%',
    itemStyle: { color: colorForWeek(index), borderRadius: [2, 2, 0, 0] },
    emphasis: { focus: 'series' as const },
  }))

  return {
    animationDuration: 400,
    grid: [
      { left: 60, right: 24, top: 66, height: 118 },
      { left: 60, right: 24, top: 216, height: 118 },
      { left: 60, right: 24, top: 392, height: 118 },
    ],
    axisPointer: { link: [{ xAxisIndex: 'all' }], lineStyle: { color: 'rgba(120,120,120,0.45)' } },
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
        if (!channel || !list.length) return ''
        const dayIndex = list[0].dataIndex
        const columns = '9px 38px 48px 58px 50px 58px 48px'
        const header = (
          `<div style="display:grid;grid-template-columns:${columns};column-gap:8px;font-size:10px;color:#64748b;font-weight:500;margin-bottom:2px;align-items:center">` +
          '<span></span><span></span><span></span>' +
          '<span style="text-align:center">花费</span><span style="text-align:center">环比</span>' +
          '<span style="text-align:center">Token</span><span style="text-align:center">请求</span></div>'
        )
        const detailRows = weeks.map((week, index) => {
          const cost = week.cost[dayIndex]
          const tokens = week.tokens[dayIndex]
          const requests = week.requests[dayIndex]
          if (cost === null && tokens === null && requests === null) return ''
          const previousCost = index > 0 ? weeks[index - 1].cost[dayIndex] : null
          return (
            `<div style="display:grid;grid-template-columns:${columns};column-gap:8px;align-items:center;line-height:22px">` +
            `<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${colorForWeek(index)}"></span>` +
            `<span style="text-align:right;color:#334155;font-weight:600;font-variant-numeric:tabular-nums">${specificDateLabel(week.key, dayIndex)}</span>` +
            `<span style="color:#94a3b8;font-size:10px">${relativeWeekLabelFromDate(week.key)}</span>` +
            `<span style="text-align:center;color:#334155;font-weight:600;font-variant-numeric:tabular-nums">${cost === null ? '—' : formatCostPrecise(cost)}</span>` +
            `<span style="text-align:center;font-variant-numeric:tabular-nums">${deltaHtml(previousCost, cost)}</span>` +
            `<span style="text-align:center;color:#334155;font-variant-numeric:tabular-nums">${tokens === null ? '—' : formatTokens(tokens)}</span>` +
            `<span style="text-align:center;color:#334155;font-variant-numeric:tabular-nums">${requests === null ? '—' : Math.round(requests).toLocaleString('zh-CN')}</span></div>`
          )
        }).join('')
        return (
          `<div style="margin-bottom:3px"><strong style="font-size:13px;color:#334155">${escapeHtml(channel.name)}</strong></div>` +
          `<div style="margin-bottom:6px;color:#64748b;font-size:11px">${WEEKDAY_LABELS[dayIndex] || ''}</div>` +
          header + detailRows
        )
      },
    },
    legend: {
      data: legendData,
      top: 6,
      left: 'center',
      icon: 'roundRect',
      itemWidth: 22,
      itemHeight: 10,
      itemGap: 18,
      textStyle: { color: labelColor, fontSize: 11 },
      formatter: (name: string) => {
        const week = weeks.find(item => weekRangeLabel(item.key) === name)
        return week ? `${name} ${relativeWeekLabelFromDate(week.key)}` : name
      },
    },
    title: [
      { text: '额度花费 (¥)', left: 60, top: 50, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
      { text: 'Token 总量', left: 60, top: 200, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
      { text: '请求数', left: 60, top: 376, textStyle: { fontSize: 11, color: labelColor, fontWeight: 'normal' as const } },
    ],
    xAxis: [0, 1, 2].map(gridIndex => ({
      type: 'category' as const,
      gridIndex,
      data: WEEKDAY_LABELS,
      boundaryGap: true,
      axisLine,
      axisTick: { show: false },
      axisLabel: { color: labelColor, fontSize: 10, show: gridIndex === 2 },
    })),
    yAxis: [
      {
        type: 'value' as const,
        gridIndex: 0,
        axisLabel: { color: labelColor, fontSize: 10, formatter: (value: number) => compactCost(value * QUOTA_PER_YUAN) },
        splitLine,
      },
      {
        type: 'value' as const,
        gridIndex: 1,
        axisLabel: { color: labelColor, fontSize: 10, formatter: (value: number) => formatTokens(value) },
        splitLine,
      },
      {
        type: 'value' as const,
        gridIndex: 2,
        minInterval: 1,
        axisLabel: { color: labelColor, fontSize: 10, formatter: (value: number) => formatTokens(value) },
        splitLine,
      },
    ],
    series: [
      ...makeBarSeries(0, week => week.cost.map(value => value === null ? null : value / QUOTA_PER_YUAN)),
      ...makeBarSeries(1, week => week.tokens),
      ...makeBarSeries(2, week => week.requests),
    ],
  }
}
