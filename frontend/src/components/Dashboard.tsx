import { lazy, Suspense, useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { useAuth } from '../contexts/AuthContext'
import { useToast } from './Toast'
import type { ChannelCostTrendData } from './ChannelSpendComparison'
import { Users, Key, Server, Box, Ticket, Zap, Crown, Loader2, RefreshCw, Activity, BarChart3, Clock, Database, Timer, ChevronDown, Hash, ArrowDownToLine, ArrowUpFromLine } from 'lucide-react'
import { Card, CardContent } from './ui/card'
import { Button } from './ui/button'
import { cn } from '../lib/utils'
import { formatCostPrecise, formatNumber } from '../lib/format'
import { MOCK_MODE } from '../lib/env'
import { mockDashboardData, mockModelStatus } from './mockData'

const SpendAnalytics = lazy(() => import('./SpendAnalytics').then(module => ({ default: module.SpendAnalytics })))
const WeeklyPatternAnalytics = lazy(() => import('./WeeklyPatternAnalytics').then(module => ({ default: module.WeeklyPatternAnalytics })))
const ChannelSpendComparison = lazy(() => import('./ChannelSpendComparison').then(module => ({ default: module.ChannelSpendComparison })))

type RefreshInterval = 0 | 30 | 60 | 120 | 300 // 秒，0表示关闭
const SPEND_TRENDS_CACHE_KEY = `dashboard_spend_hourly_v2:${MOCK_MODE ? 'mock' : 'live'}`
const HOURLY_REFRESH_DELAY_MS = 5_000

interface SystemOverview {
  total_users: number
  active_users: number
  total_tokens: number
  active_tokens: number
  total_channels: number
  active_channels: number
  total_models: number
  total_redemptions: number
  unused_redemptions: number
}

interface UsageStatistics {
  period: string
  total_requests: number
  total_quota_used: number
  total_prompt_tokens: number
  total_completion_tokens: number
  average_response_time: number
}

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

interface AnalyticsSummary {
  request_king: { user_id: number; username: string; request_count: number } | null
  quota_king: { user_id: number; username: string; quota_used: number } | null
}

interface SystemInfo {
  scale: string
  is_large_system: boolean
  metrics: {
    total_users: number
    logs_24h: number
    total_logs: number
  }
  tips?: {
    refresh_warning: boolean
    logs_24h_formatted: string
    message: string
  }
}

interface RefreshEstimate {
  show_estimate: boolean
  scale?: string
  estimated_logs?: number
  estimated_logs_formatted?: string
  estimated_seconds?: number
  estimated_time_formatted?: string
  warning?: string
}

type PeriodType = 'today' | 'week' | 'month'

function localDayKey(date = new Date()): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function localHourKey(date: Date): string {
  return `${localDayKey(date)} ${String(date.getHours()).padStart(2, '0')}:00`
}

function trendDayKey(trend: DailyTrend): string {
  if (trend.hour) return trend.hour.slice(0, 10)
  if (trend.timestamp) return localDayKey(new Date(trend.timestamp * 1000))
  return ''
}

function trendHourKey(trend: DailyTrend): string {
  if (trend.hour) return trend.hour
  if (trend.timestamp) return localHourKey(new Date(trend.timestamp * 1000))
  return ''
}

function readCachedDailyTrends(): DailyTrend[] {
  try {
    const raw = localStorage.getItem(SPEND_TRENDS_CACHE_KEY)
    if (!raw) return []
    const cached = JSON.parse(raw) as { day?: string; trends?: DailyTrend[] }
    if (cached.day !== localDayKey() || !Array.isArray(cached.trends)) return []
    return cached.trends.filter((trend) => trend && trendDayKey(trend) === cached.day)
  } catch {
    return []
  }
}

function mergeDailyTrends(current: DailyTrend[], incoming: DailyTrend[]): DailyTrend[] {
  const today = localDayKey()
  const byHour = new Map<string, DailyTrend>()

  for (const trend of [...current, ...incoming]) {
    const key = trendHourKey(trend)
    if (key && trendDayKey(trend) === today) byHour.set(key, trend)
  }

  return [...byHour.values()].sort((a, b) => trendHourKey(a).localeCompare(trendHourKey(b)))
}

function cacheDailyTrends(trends: DailyTrend[]): void {
  try {
    localStorage.setItem(SPEND_TRENDS_CACHE_KEY, JSON.stringify({ day: localDayKey(), trends }))
  } catch {
    // The in-memory chart still works if storage is unavailable or full.
  }
}

function msUntilNextHourlyRefresh(now = new Date()): number {
  const next = new Date(now)
  next.setHours(next.getHours() + 1, 0, 0, HOURLY_REFRESH_DELAY_MS)
  return Math.max(1_000, next.getTime() - now.getTime())
}

export function Dashboard() {
  const { token } = useAuth()
  const { showToast } = useToast()
  const [overview, setOverview] = useState<SystemOverview | null>(null)
  const [usage, setUsage] = useState<UsageStatistics | null>(null)
  const [completedHourlyTrends, setCompletedHourlyTrends] = useState<DailyTrend[]>(readCachedDailyTrends)
  const completedHourlyTrendsRef = useRef(completedHourlyTrends)
  const [currentHourlyTrends, setCurrentHourlyTrends] = useState<DailyTrend[]>([])
  const currentHourlyRequestRef = useRef<Promise<boolean> | null>(null)
  const completedTodayRequestRef = useRef<Promise<void> | null>(null)
  // useMemo 保持引用稳定:自动刷新倒计时每秒触发 Dashboard 重渲染,
  // 若每次渲染都新建数组,下游图表 option 会随之重建(setOption notMerge),
  // 导致正在显示的 tooltip 被销毁、legend 选中状态被重置。
  const dailyTrends = useMemo(
    () => mergeDailyTrends(completedHourlyTrends, currentHourlyTrends),
    [completedHourlyTrends, currentHourlyTrends],
  )
  const [trendsLoading, setTrendsLoading] = useState(true)
  // 近 28 天按天趋势,供「周内规律分析」使用(独立于当天小时趋势)
  const [weeklyTrends, setWeeklyTrends] = useState<DailyTrend[]>([])
  const [weeklyLoading, setWeeklyLoading] = useState(true)
  const [channelSpendData, setChannelSpendData] = useState<ChannelCostTrendData | null>(null)
  const [channelSpendLoading, setChannelSpendLoading] = useState(true)
  const [analyticsSummary, setAnalyticsSummary] = useState<AnalyticsSummary | null>(null)
  const [analyticsLoaded, setAnalyticsLoaded] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  // 固定「当天 · 按小时」视角(不再提供 today/week/month 切换)
  const [period] = useState<PeriodType>('today')
  const [loadError, setLoadError] = useState<string | null>(null)

  const DASHBOARD_REFRESH_KEY = 'dashboard_refresh_interval'
  const [refreshInterval, setRefreshInterval] = useState<RefreshInterval>(() => {
    const saved = localStorage.getItem(DASHBOARD_REFRESH_KEY)
    return saved ? (parseInt(saved, 10) as RefreshInterval) : 0
  })
  const [countdown, setCountdown] = useState<number>(() => {
    const saved = localStorage.getItem(DASHBOARD_REFRESH_KEY)
    return saved ? parseInt(saved, 10) : 0
  })

  const [lastRefreshTime, setLastRefreshTime] = useState<Date | null>(null)
  const [showIntervalDropdown, setShowIntervalDropdown] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)

  // Ref to always call the latest handleRefresh from timer
  const handleRefreshRef = useRef<() => void>(() => { })

  // 大型系统刷新提示相关状态
  const [systemInfo, setSystemInfo] = useState<SystemInfo | null>(null)
  const [refreshEstimate, setRefreshEstimate] = useState<RefreshEstimate | null>(null)
  const [showRefreshConfirm, setShowRefreshConfirm] = useState(false)
  const [refreshProgress, setRefreshProgress] = useState<string | null>(null)

  const apiUrl = import.meta.env.VITE_API_URL || ''
  const requestTimeoutMs = 30_000
  const getAuthHeaders = useCallback(() => ({
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  }), [token])

  // ---- Mock helpers ----
  const delay = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))

  const mockOverview = useCallback(async () => {
    await delay(200)
    setOverview(mockDashboardData.overview)
    return true
  }, [])

  const mockUsage = useCallback(async () => {
    await delay(150)
    setUsage(mockDashboardData.usage)
    return true
  }, [])

  const mockAnalyticsSummary = useCallback(async () => {
    await delay(100)
    const sortedByRequest = [...mockDashboardData.topUsers].sort((a, b) => b.request_count - a.request_count)
    const sortedByQuota = [...mockDashboardData.topUsers].sort((a, b) => b.quota_used - a.quota_used)
    setAnalyticsSummary({
      request_king: sortedByRequest.length > 0 ? {
        user_id: sortedByRequest[0].user_id,
        username: sortedByRequest[0].username,
        request_count: sortedByRequest[0].request_count,
      } : null,
      quota_king: sortedByQuota.length > 0 ? {
        user_id: sortedByQuota[0].user_id,
        username: sortedByQuota[0].username,
        quota_used: sortedByQuota[0].quota_used,
      } : null,
    })
    setAnalyticsLoaded(true)
    return true
  }, [])

  // ---- End mock helpers ----

  const fetchOverview = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    try {
      const cacheParam = noCache ? '&no_cache=true' : ''
      const response = await fetch(
        `${apiUrl}/api/dashboard/overview?period=${period}${cacheParam}`,
        { headers: getAuthHeaders(), signal },
      )
      const data = await response.json()
      if (data.success) {
        setOverview(data.data)
        return true
      }
    } catch (error) { console.error('Failed to fetch overview:', error) }
    return false
  }, [apiUrl, getAuthHeaders, period])

  const fetchUsage = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    try {
      const cacheParam = noCache ? '&no_cache=true' : ''
      const response = await fetch(
        `${apiUrl}/api/dashboard/usage?period=${period}${cacheParam}`,
        { headers: getAuthHeaders(), signal },
      )
      const data = await response.json()
      if (data.success) {
        setUsage(data.data)
        return true
      }
    } catch (error) { console.error('Failed to fetch usage:', error) }
    return false
  }, [apiUrl, getAuthHeaders, period])

  // 近 28 天按天趋势(周内规律分析):一次性拉取,不参与小时级轮询。
  const fetchWeeklyTrends = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    try {
      if (MOCK_MODE) {
        await delay(180)
        setWeeklyTrends(mockDashboardData.getDailyHistory(28))
        return true
      }
      const cacheParam = noCache ? '&no_cache=true' : ''
      const response = await fetch(
        `${apiUrl}/api/dashboard/trends/daily?days=28${cacheParam}`,
        { headers: getAuthHeaders(), signal },
      )
      const data = await response.json()
      if (data.success && Array.isArray(data.data)) {
        setWeeklyTrends(data.data as DailyTrend[])
        return true
      }
    } catch (error) {
      if (!signal?.aborted) console.error('Failed to fetch weekly trends:', error)
    }
    return false
  }, [apiUrl, getAuthHeaders])

  const fetchChannelSpend = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    setChannelSpendLoading(true)
    try {
      if (MOCK_MODE) {
        await delay(180)
        if (signal?.aborted) return false
        setChannelSpendData(mockModelStatus.getChannelCostTrends(28, '') as ChannelCostTrendData)
        return true
      }

      const response = await fetch(
        `${apiUrl}/api/model-status/channels/cost-trends?days=28`,
        { headers: getAuthHeaders(), signal },
      )
      const result = await response.json()
      if (result.success && result.data && Array.isArray(result.data.channels)) {
        setChannelSpendData(result.data as ChannelCostTrendData)
        return true
      }
      setChannelSpendData(null)
    } catch (error) {
      if (!signal?.aborted) {
        console.error('Failed to fetch channel spend comparison:', error)
        setChannelSpendData(null)
      }
    } finally {
      if (!signal?.aborted) setChannelSpendLoading(false)
    }
    return false
  }, [apiUrl, getAuthHeaders])

  const mergeCompletedHourlyTrends = useCallback((trends: DailyTrend[]) => {
    const completedKeys = new Set(trends.map(trendHourKey).filter(Boolean))
    const merged = mergeDailyTrends(completedHourlyTrendsRef.current, trends)
    completedHourlyTrendsRef.current = merged
    setCompletedHourlyTrends(merged)
    cacheDailyTrends(merged)
    setCurrentHourlyTrends(current => current.filter(trend => !completedKeys.has(trendHourKey(trend))))
  }, [])

  const replaceCurrentHourlyTrend = useCallback((trends: DailyTrend[]) => {
    const currentHour = localHourKey(new Date())
    setCurrentHourlyTrends(trends.filter(trend => trendHourKey(trend) === currentHour))
  }, [])

  // 当前小时独立短轮询，只覆盖尚未结算的实时数据。
  const fetchCurrentHourlyTrend = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    if (currentHourlyRequestRef.current) return currentHourlyRequestRef.current

    const request = (async () => {
      try {
        if (MOCK_MODE) {
          await delay(120)
          const currentHour = localHourKey(new Date())
          replaceCurrentHourlyTrend(mockDashboardData.getToday().filter(item => item.hour === currentHour))
          return true
        }

        const cacheParam = noCache ? '?no_cache=true' : ''
        const response = await fetch(
          `${apiUrl}/api/dashboard/trends/hourly/current${cacheParam}`,
          { headers: getAuthHeaders(), signal },
        )
        const data = await response.json()
        if (data.success && Array.isArray(data.data)) {
          replaceCurrentHourlyTrend(data.data)
          return true
        }
      } catch (error) {
        if (!signal?.aborted) console.error('Failed to fetch current hourly trend:', error)
      }
      return false
    })()

    currentHourlyRequestRef.current = request
    try {
      return await request
    } finally {
      if (currentHourlyRequestRef.current === request) currentHourlyRequestRef.current = null
    }
  }, [apiUrl, getAuthHeaders, replaceCurrentHourlyTrend])

  // 首次进入或本地缓存缺失时，一次请求补齐今天所有已完成小时。
  const fetchCompletedTodayTrends = useCallback(async (signal?: AbortSignal): Promise<void> => {
    if (completedTodayRequestRef.current) return completedTodayRequestRef.current

    const request = (async () => {
      const today = localDayKey()
      const cursor = new Date()
      cursor.setMinutes(0, 0, 0)
      cursor.setHours(cursor.getHours() - 1)
      const expectedHours: string[] = []

      while (localDayKey(cursor) === today) {
        expectedHours.push(localHourKey(cursor))
        cursor.setHours(cursor.getHours() - 1)
      }

      const cachedHours = new Set(completedHourlyTrendsRef.current.map(trendHourKey))
      if (expectedHours.every(hour => cachedHours.has(hour))) return

      try {
        if (MOCK_MODE) {
          await delay(80)
          if (signal?.aborted) return
          const expected = new Set(expectedHours)
          mergeCompletedHourlyTrends(mockDashboardData.getToday().filter(item => expected.has(trendHourKey(item))))
          return
        }

        const response = await fetch(
          `${apiUrl}/api/dashboard/trends/hourly/completed/today`,
          { headers: getAuthHeaders(), signal },
        )
        const data = await response.json()
        if (data.success && Array.isArray(data.data)) {
          mergeCompletedHourlyTrends(data.data)
        }
      } catch (error) {
        if (!signal?.aborted) console.error('Failed to fetch completed hourly trends:', error)
      }
    })()

    completedTodayRequestRef.current = request
    try {
      await request
    } finally {
      if (completedTodayRequestRef.current === request) completedTodayRequestRef.current = null
    }
  }, [apiUrl, getAuthHeaders, mergeCompletedHourlyTrends])

  // 整点后只结算上一个完整小时一次。
  const fetchPreviousHourlyTrend = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    try {
      if (MOCK_MODE) {
        await delay(200)
        const previousHour = new Date(Date.now() - 60 * 60 * 1000)
        const trend = mockDashboardData.getToday().filter(item => item.hour === localHourKey(previousHour))
        mergeCompletedHourlyTrends(trend)
        return true
      }

      const response = await fetch(
        `${apiUrl}/api/dashboard/trends/hourly/previous`,
        { headers: getAuthHeaders(), signal },
      )
      const data = await response.json()
      if (data.success && Array.isArray(data.data)) {
        mergeCompletedHourlyTrends(data.data)
        return true
      }
    } catch (error) {
      if (!signal?.aborted) console.error('Failed to fetch previous hourly trend:', error)
    }
    return false
  }, [apiUrl, getAuthHeaders, mergeCompletedHourlyTrends])

  const fetchAnalyticsSummary = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    try {
      const cacheParam = noCache ? '&no_cache=true' : ''
      const response = await fetch(
        `${apiUrl}/api/dashboard/top-users?period=${period}&limit=10${cacheParam}`,
        { headers: getAuthHeaders(), signal },
      )
      const data = await response.json()

      if (data.success && Array.isArray(data.data) && data.data.length > 0) {
        const sortedByRequest = [...data.data].sort((a: any, b: any) => b.request_count - a.request_count)
        const sortedByQuota = [...data.data].sort((a: any, b: any) => b.quota_used - a.quota_used)

        setAnalyticsSummary({
          request_king: sortedByRequest.length > 0 ? {
            user_id: sortedByRequest[0].user_id,
            username: sortedByRequest[0].username,
            request_count: sortedByRequest[0].request_count,
          } : null,
          quota_king: sortedByQuota.length > 0 ? {
            user_id: sortedByQuota[0].user_id,
            username: sortedByQuota[0].username,
            quota_used: sortedByQuota[0].quota_used,
          } : null,
        })
      } else {
        setAnalyticsSummary(null)
      }
      return Boolean(data.success)
    } catch (error) { console.error('Failed to fetch analytics summary:', error) }
    finally { if (!signal?.aborted) setAnalyticsLoaded(true) }
    return false
  }, [apiUrl, getAuthHeaders, period])

  const fetchAll = useCallback(async (noCache = false, signal?: AbortSignal): Promise<boolean> => {
    if (MOCK_MODE) {
      const results = await Promise.all([
        mockOverview(), mockUsage(), mockAnalyticsSummary(),
      ])
      return results.every(Boolean)
    }
    const results = await Promise.all([
      fetchOverview(noCache, signal),
      fetchUsage(noCache, signal),
      fetchAnalyticsSummary(noCache, signal),
    ])
    return results.every(Boolean)
  }, [fetchOverview, fetchUsage, fetchAnalyticsSummary, mockOverview, mockUsage, mockAnalyticsSummary])

  const refreshAll = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    if (MOCK_MODE) {
      const results = await Promise.all([
        mockOverview(), mockUsage(), mockAnalyticsSummary(), fetchCurrentHourlyTrend(true, signal), fetchChannelSpend(signal),
      ])
      return results.every(Boolean)
    }
    const results = await Promise.all([
      fetchOverview(true, signal),
      fetchUsage(true, signal),
      fetchAnalyticsSummary(true, signal),
      fetchCurrentHourlyTrend(true, signal),
      fetchChannelSpend(signal),
    ])
    return results.every(Boolean)
  }, [fetchOverview, fetchUsage, fetchAnalyticsSummary, fetchCurrentHourlyTrend, fetchChannelSpend, mockOverview, mockUsage, mockAnalyticsSummary])

  useEffect(() => {
    const controller = new AbortController()
    let boundaryTimerId: number | undefined
    let active = true

    const scheduleNextBoundary = () => {
      boundaryTimerId = window.setTimeout(async () => {
        if (document.visibilityState === 'visible') {
          await fetchPreviousHourlyTrend(controller.signal)
          await fetchCurrentHourlyTrend(true, controller.signal)
        }
        if (active) scheduleNextBoundary()
      }, msUntilNextHourlyRefresh())
    }

    const loadTodayTrends = async () => {
      try {
        await Promise.all([
          fetchCurrentHourlyTrend(false, controller.signal),
          fetchCompletedTodayTrends(controller.signal),
        ])
      } finally {
        if (active) setTrendsLoading(false)
      }
    }

    const loadWeekly = async () => {
      try {
        await fetchWeeklyTrends(false, controller.signal)
      } finally {
        if (active) setWeeklyLoading(false)
      }
    }

    const handleVisibilityChange = () => {
      if (document.visibilityState !== 'visible') return
      void fetchCurrentHourlyTrend(false, controller.signal)
      void fetchCompletedTodayTrends(controller.signal)
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    void loadTodayTrends()
    void loadWeekly()
    scheduleNextBoundary()

    return () => {
      active = false
      currentHourlyRequestRef.current = null
      completedTodayRequestRef.current = null
      controller.abort()
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      if (boundaryTimerId !== undefined) window.clearTimeout(boundaryTimerId)
    }
  }, [fetchCompletedTodayTrends, fetchCurrentHourlyTrend, fetchPreviousHourlyTrend, fetchWeeklyTrends])

  useEffect(() => {
    const controller = new AbortController()
    void fetchChannelSpend(controller.signal)
    return () => controller.abort()
  }, [fetchChannelSpend])

  // 获取系统规模信息（仅首次加载）
  const fetchSystemInfo = useCallback(async () => {
    if (MOCK_MODE) return
    try {
      const response = await fetch(
        `${apiUrl}/api/dashboard/system-info`,
        { headers: getAuthHeaders() },
      )
      const data = await response.json()
      if (data.success) {
        setSystemInfo(data.data)
      }
    } catch (error) {
      console.error('Failed to fetch system info:', error)
    }
  }, [apiUrl, getAuthHeaders])

  // 获取刷新预估信息
  const fetchRefreshEstimate = useCallback(async () => {
    try {
      const response = await fetch(
        `${apiUrl}/api/dashboard/refresh-estimate?period=${period}`,
        { headers: getAuthHeaders() },
      )
      const data = await response.json()
      if (data.success) {
        setRefreshEstimate(data.data)
      }
    } catch (error) {
      console.error('Failed to fetch refresh estimate:', error)
    }
  }, [apiUrl, getAuthHeaders, period])

  // 首次加载时获取系统信息
  useEffect(() => {
    fetchSystemInfo()
  }, [fetchSystemInfo])

  useEffect(() => {
    const controller = new AbortController()
    let mounted = true

    const loadData = async () => {
      setLoadError(null)

      const timeoutId = window.setTimeout(() => {
        if (mounted) setLoadError('仪表盘加载超时，请稍后重试（可能是数据库负载过高）')
        controller.abort()
      }, requestTimeoutMs)

      try {
        const ok = await fetchAll(false, controller.signal)
        if (!ok && mounted && !controller.signal.aborted) {
          setLoadError('部分数据加载失败，可重试缺失内容')
        }
      } finally {
        window.clearTimeout(timeoutId)
      }
    }
    loadData()

    return () => {
      mounted = false
      controller.abort()
    }
  }, [fetchAll, requestTimeoutMs])

  const handleRetry = async () => {
    setRefreshing(true)
    setLoadError(null)

    const controller = new AbortController()
    const timeoutId = window.setTimeout(() => controller.abort(), requestTimeoutMs)

    try {
      const ok = await fetchAll(false, controller.signal)
      if (!ok || controller.signal.aborted) {
        showToast('error', '重试失败，请稍后再试')
        setLoadError('重试失败，请稍后再试（可能是数据库负载过高）')
      }
    } finally {
      window.clearTimeout(timeoutId)
      setRefreshing(false)
      controller.abort()
    }
  }

  const handleRefresh = async () => {
    // 大型系统：先获取预估信息并显示确认
    if (systemInfo?.is_large_system && !showRefreshConfirm) {
      await fetchRefreshEstimate()
      setShowRefreshConfirm(true)
      return
    }

    // 关闭确认对话框
    setShowRefreshConfirm(false)
    setRefreshing(true)
    setLoadError(null)

    // 大型系统显示进度
    if (systemInfo?.is_large_system) {
      setRefreshProgress('正在刷新数据...')
    }

    const controller = new AbortController()
    // 大型系统给更长的超时时间
    const timeout = systemInfo?.is_large_system ? 60_000 : requestTimeoutMs
    const timeoutId = window.setTimeout(() => controller.abort(), timeout)

    try {
      const ok = await refreshAll(controller.signal)
      if (ok && !controller.signal.aborted) {
        showToast('success', '数据已刷新')
        setLastRefreshTime(new Date())
        if (refreshInterval > 0) {
          setCountdown(refreshInterval)
        }
      } else {
        showToast('error', '刷新失败，请稍后再试')
        setLoadError('刷新失败，请稍后再试（可能是数据库负载过高）')
      }
    } finally {
      window.clearTimeout(timeoutId)
      setRefreshing(false)
      setRefreshProgress(null)
      controller.abort()
    }
  }

  // Keep ref in sync with latest handleRefresh
  useEffect(() => {
    handleRefreshRef.current = handleRefresh
  })

  // 取消刷新确认
  const handleCancelRefresh = () => {
    setShowRefreshConfirm(false)
    setRefreshEstimate(null)
  }

  // 点击外部关闭下拉菜单
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setShowIntervalDropdown(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  // 自动刷新倒计时 - 使用 ref 避免过期闭包
  useEffect(() => {
    if (refreshInterval === 0) {
      setCountdown(0)
      return
    }

    const timer = setInterval(() => {
      setCountdown(prev => {
        if (prev <= 1) {
          // 通过 ref 调用最新的 handleRefresh，确保使用当前 period
          handleRefreshRef.current()
          return refreshInterval
        }
        return prev - 1
      })
    }, 1000)

    return () => clearInterval(timer)
  }, [refreshInterval])

  // 设置刷新间隔时初始化倒计时
  const handleSetRefreshInterval = (interval: RefreshInterval) => {
    setRefreshInterval(interval)
    if (interval > 0) {
      setCountdown(interval)
      localStorage.setItem(DASHBOARD_REFRESH_KEY, interval.toString())
      showToast('success', `自动刷新已设置为 ${getIntervalLabel(interval)}`)
    } else {
      localStorage.removeItem(DASHBOARD_REFRESH_KEY)
      showToast('info', '自动刷新已关闭')
    }
    setShowIntervalDropdown(false)
  }

  const formatCountdown = (seconds: number) => {
    const mins = Math.floor(seconds / 60)
    const secs = seconds % 60
    return mins > 0 ? `${mins}:${secs.toString().padStart(2, '0')}` : `${secs}s`
  }

  const formatLastRefreshTime = (date: Date | null) => {
    if (!date) return '从未'
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  const getIntervalLabel = (interval: RefreshInterval) => {
    switch (interval) {
      case 0: return '关闭'
      case 30: return '30秒'
      case 60: return '1分钟'
      case 120: return '2分钟'
      case 300: return '5分钟'
      default: return '关闭'
    }
  }

  const getPeriodLabel = () => period === 'today' ? '当天' : period === 'week' ? '本周' : '本月'

  return (
    <div className="space-y-8 animate-in fade-in duration-500">
      {/* 大型系统刷新确认对话框 */}
      {showRefreshConfirm && refreshEstimate?.show_estimate && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-background border rounded-lg shadow-lg p-6 max-w-md mx-4 animate-in zoom-in-95 duration-200">
            <div className="flex items-center gap-3 mb-4">
              <div className="h-10 w-10 rounded-full bg-yellow-100 dark:bg-yellow-900/30 flex items-center justify-center">
                <Database className="h-5 w-5 text-yellow-600 dark:text-yellow-400" />
              </div>
              <div>
                <h3 className="font-semibold">确认刷新数据</h3>
                <p className="text-sm text-muted-foreground">{refreshEstimate.scale === 'large' ? '大型系统' : '超大型系统'}</p>
              </div>
            </div>

            <div className="space-y-3 mb-6">
              <div className="bg-muted/50 rounded-lg p-4 space-y-2">
                <div className="flex justify-between text-sm">
                  <span className="text-muted-foreground">预计扫描日志</span>
                  <span className="font-medium">{refreshEstimate.estimated_logs_formatted} 条</span>
                </div>
                <div className="flex justify-between text-sm">
                  <span className="text-muted-foreground">预计耗时</span>
                  <span className="font-medium">{refreshEstimate.estimated_time_formatted}</span>
                </div>
              </div>

              {refreshEstimate.warning && (
                <p className="text-xs text-yellow-600 dark:text-yellow-400 flex items-center gap-1">
                  <Activity className="h-3 w-3" />
                  {refreshEstimate.warning}
                </p>
              )}
            </div>

            <div className="flex gap-3">
              <Button
                variant="outline"
                className="flex-1"
                onClick={handleCancelRefresh}
              >
                取消
              </Button>
              <Button
                className="flex-1"
                onClick={handleRefresh}
              >
                确认刷新
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* 大型系统刷新进度覆盖层 */}
      {refreshing && refreshProgress && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-background border rounded-lg shadow-lg p-8 max-w-sm mx-4 animate-in zoom-in-95 duration-200">
            <div className="flex flex-col items-center gap-4">
              <Loader2 className="h-12 w-12 animate-spin text-primary" />
              <div className="text-center">
                <p className="font-medium">{refreshProgress}</p>
                <p className="text-sm text-muted-foreground mt-1">
                  正在查询 {refreshEstimate?.estimated_logs_formatted || '大量'} 条日志数据
                </p>
                <p className="text-xs text-muted-foreground mt-2">
                  预计需要 {refreshEstimate?.estimated_time_formatted || '较长时间'}，请耐心等待
                </p>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Header Actions */}
      <div className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
        <div>
          <h2 className="text-3xl font-bold tracking-tight">仪表盘</h2>
          <p className="text-muted-foreground mt-1">系统运行状态与实时数据概览</p>
        </div>
        <div className="flex items-center gap-3 flex-wrap">
          {/* 刷新按钮和自动刷新控制 */}
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={handleRefresh} disabled={refreshing} className="h-9">
              <RefreshCw className={cn("h-4 w-4 mr-2", refreshing && "animate-spin")} />
              {refreshing ? '刷新中...' : '刷新'}
            </Button>

            {/* 自动刷新下拉菜单 */}
            <div className="relative" ref={dropdownRef}>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setShowIntervalDropdown(!showIntervalDropdown)}
                className="h-9 min-w-[100px]"
              >
                <Timer className="h-4 w-4 mr-2" />
                {refreshInterval > 0 ? (
                  <span className="flex items-center gap-1">
                    <span className="text-primary font-medium">{formatCountdown(countdown)}</span>
                  </span>
                ) : (
                  '自动刷新'
                )}
                <ChevronDown className="h-3 w-3 ml-1" />
              </Button>

              {showIntervalDropdown && (
                <div className="absolute right-0 mt-1 w-48 bg-popover border rounded-md shadow-lg z-50">
                  <div className="p-2 border-b">
                    <p className="text-xs text-muted-foreground">刷新间隔</p>
                  </div>
                  <div className="p-1">
                    {([0, 30, 60, 120, 300] as RefreshInterval[]).map((interval) => (
                      <button
                        key={interval}
                        onClick={() => handleSetRefreshInterval(interval)}
                        className={cn(
                          "w-full text-left px-3 py-2 text-sm rounded hover:bg-accent transition-colors",
                          refreshInterval === interval && "bg-accent text-accent-foreground"
                        )}
                      >
                        {getIntervalLabel(interval)}
                      </button>
                    ))}
                  </div>
                  {lastRefreshTime && (
                    <div className="p-2 border-t">
                      <p className="text-xs text-muted-foreground">
                        上次刷新: {formatLastRefreshTime(lastRefreshTime)}
                      </p>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
      </div>

      {loadError && (
        <div className="flex flex-col gap-3 border-y border-amber-200 bg-amber-50/70 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/20 dark:text-amber-200 sm:flex-row sm:items-center sm:justify-between" role="alert">
          <span>{loadError}</span>
          <Button variant="outline" size="sm" onClick={handleRetry} disabled={refreshing} className="self-start sm:self-auto">
            <RefreshCw className={cn("mr-2 h-4 w-4", refreshing && "animate-spin")} />
            {refreshing ? '重试中...' : '重试'}
          </Button>
        </div>
      )}

      {/* System Overview Section */}
      <section className="space-y-4">
        <h3 className="text-lg font-semibold flex items-center gap-2">
          <Database className="w-5 h-5 text-primary" />
          平台资源
        </h3>
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
          <StatCard
            title="用户总数"
            value={overview?.total_users || 0}
            subValue={`${overview?.active_users || 0} 活跃(${getPeriodLabel()})`}
            icon={Users}
            color="blue"
            loading={!overview}
          />
          <StatCard
            title="令牌总数"
            value={overview?.total_tokens || 0}
            subValue={`${overview?.active_tokens || 0} 活跃(${getPeriodLabel()})`}
            icon={Key}
            color="emerald"
            loading={!overview}
          />
          <StatCard
            title="渠道总数"
            value={overview?.total_channels || 0}
            subValue={`${overview?.active_channels || 0} 在线`}
            icon={Server}
            color="purple"
            loading={!overview}
          />
          <StatCard
            title="模型数量"
            value={overview?.total_models || 0}
            subValue="可用模型"
            icon={Box}
            color="orange"
            loading={!overview}
          />
          <StatCard
            title="兑换码"
            value={overview?.total_redemptions || 0}
            subValue={`${overview?.unused_redemptions || 0} 未用`}
            icon={Ticket}
            color="pink"
            loading={!overview}
          />
        </div>
      </section>

      {/* Usage Statistics Section */}
      <section className="space-y-4">
        <h3 className="text-lg font-semibold flex items-center gap-2">
          <Activity className="w-5 h-5 text-primary" />
          流量分析 ({getPeriodLabel()})
        </h3>
        <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
          <StatCard
            title="请求总数"
            value={formatNumber(usage?.total_requests || 0)}
            rawValue={usage?.total_requests || 0}
            icon={BarChart3}
            color="indigo"
            variant="compact"
            loading={!usage}
          />
          <StatCard
            title="消耗额度"
            value={formatCostPrecise(usage?.total_quota_used || 0)}
            rawValue={usage?.total_quota_used ? usage.total_quota_used / 500000 : 0}
            icon={Zap}
            color="amber"
            variant="compact"
            loading={!usage}
          />
          <StatCard
            title="总 Token"
            value={formatNumber(Number(usage?.total_prompt_tokens || 0) + Number(usage?.total_completion_tokens || 0))}
            rawValue={Number(usage?.total_prompt_tokens || 0) + Number(usage?.total_completion_tokens || 0)}
            icon={Hash}
            color="purple"
            variant="compact"
            loading={!usage}
          />
          <StatCard
            title="输入 Token"
            value={formatNumber(Number(usage?.total_prompt_tokens || 0))}
            rawValue={Number(usage?.total_prompt_tokens || 0)}
            icon={ArrowDownToLine}
            color="cyan"
            variant="compact"
            loading={!usage}
          />
          <StatCard
            title="输出 Token"
            value={formatNumber(Number(usage?.total_completion_tokens || 0))}
            rawValue={Number(usage?.total_completion_tokens || 0)}
            icon={ArrowUpFromLine}
            color="teal"
            variant="compact"
            loading={!usage}
          />
          <StatCard
            title="平均响应"
            value={`${(usage?.average_response_time || 0).toFixed(3)}ms`}
            icon={Clock}
            color="rose"
            variant="compact"
            loading={!usage}
          />
        </div>
      </section>


      {/* Spend Analytics — 当天按小时:上「每小时花费折线」+ 下「Token 组成堆叠柱」,共用小时横轴 */}
      <Suspense fallback={<DashboardChartFallback height={548} />}>
        <SpendAnalytics
          dailyTrends={dailyTrends}
          loading={trendsLoading}
        />
      </Suspense>

      <Suspense fallback={<DashboardChartFallback height={430} />}>
        <ChannelSpendComparison
          data={channelSpendData}
          loading={channelSpendLoading}
        />
      </Suspense>

      {/* Weekly Pattern — 近4周按星期几:花费 / Token / 缓存命中率 三个子图,4条线对比周与周 */}
      <Suspense fallback={<DashboardChartFallback height={520} />}>
        <WeeklyPatternAnalytics
          dailyTrends={weeklyTrends}
          loading={weeklyLoading}
        />
      </Suspense>

      {/* Analytics Kings */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <KingCard
          title="请求之王"
          subtitle={`${getPeriodLabel()}内请求数最多`}
          icon={Zap}
          user={analyticsSummary?.request_king}
          valueLabel="总请求数"
          value={analyticsSummary?.request_king?.request_count.toLocaleString()}
          gradient="from-blue-600 to-indigo-600"
          accentColor="text-blue-100"
          loading={!analyticsLoaded}
        />
        <KingCard
          title="土豪榜首"
          subtitle={`${getPeriodLabel()}内消耗额度最多`}
          icon={Crown}
          user={analyticsSummary?.quota_king}
          valueLabel="总消耗额度"
          value={analyticsSummary?.quota_king ? `$${(analyticsSummary.quota_king.quota_used / 500000).toFixed(2)}` : undefined}
          gradient="from-emerald-600 to-teal-600"
          accentColor="text-emerald-100"
          loading={!analyticsLoaded}
        />
      </div>
    </div>
  )
}

// --- Components ---

function DashboardChartFallback({ height }: { height: number }) {
  return (
    <Card className="border-border/50 shadow-sm">
      <CardContent className="p-6">
        <div className="mb-6 h-8 w-48 animate-pulse rounded bg-muted/40" />
        <div className="animate-pulse rounded-md bg-muted/20" style={{ height: height - 80 }} />
      </CardContent>
    </Card>
  )
}

interface StatCardProps {
  title: string
  value: number | string
  rawValue?: number  // 原始数值，用于 tooltip 显示完整数字
  subValue?: string
  icon: React.ElementType
  color: string
  variant?: 'default' | 'compact'
  customLabel?: string
  loading?: boolean
}

function StatCard({ title, value, rawValue, subValue, icon: Icon, color, variant = 'default', customLabel, loading = false }: StatCardProps) {
  // Map color names to Tailwind classes
  const colorMap: Record<string, { bg: string, text: string, ring: string }> = {
    blue: { bg: 'bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300', text: 'text-blue-600', ring: 'group-hover:ring-blue-200' },
    green: { bg: 'bg-green-50 text-green-700 dark:bg-green-950 dark:text-green-300', text: 'text-green-600', ring: 'group-hover:ring-green-200' },
    emerald: { bg: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300', text: 'text-emerald-600', ring: 'group-hover:ring-emerald-200' },
    purple: { bg: 'bg-purple-50 text-purple-700 dark:bg-purple-950 dark:text-purple-300', text: 'text-purple-600', ring: 'group-hover:ring-purple-200' },
    orange: { bg: 'bg-orange-50 text-orange-700 dark:bg-orange-950 dark:text-orange-300', text: 'text-orange-600', ring: 'group-hover:ring-orange-200' },
    pink: { bg: 'bg-pink-50 text-pink-700 dark:bg-pink-950 dark:text-pink-300', text: 'text-pink-600', ring: 'group-hover:ring-pink-200' },
    indigo: { bg: 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300', text: 'text-indigo-600', ring: 'group-hover:ring-indigo-200' },
    amber: { bg: 'bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300', text: 'text-amber-600', ring: 'group-hover:ring-amber-200' },
    cyan: { bg: 'bg-cyan-50 text-cyan-700 dark:bg-cyan-950 dark:text-cyan-300', text: 'text-cyan-600', ring: 'group-hover:ring-cyan-200' },
    teal: { bg: 'bg-teal-50 text-teal-700 dark:bg-teal-950 dark:text-teal-300', text: 'text-teal-600', ring: 'group-hover:ring-teal-200' },
    rose: { bg: 'bg-rose-50 text-rose-700 dark:bg-rose-950 dark:text-rose-300', text: 'text-rose-600', ring: 'group-hover:ring-rose-200' },
  }

  const theme = colorMap[color] || colorMap.blue

  if (variant === 'compact') {
    // Auto-size font based on value string length
    const valueStr = String(value)
    const fontSize = valueStr.length > 14 ? 'text-sm' : valueStr.length > 10 ? 'text-base' : valueStr.length > 7 ? 'text-lg' : 'text-xl'
    return (
      <Card className={cn("glass-card overflow-hidden hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300 group border-l-4", `border-l-${color}-500`)}>
        <CardContent className="p-4 flex items-center justify-between relative overflow-hidden">
          <div className={cn("absolute -right-4 -top-4 w-16 h-16 rounded-full opacity-10 group-hover:opacity-20 transition-opacity duration-300 blur-xl", theme.bg.split(' ')[0])} />
          <div className="space-y-1 min-w-0 flex-1 mr-2 relative z-10">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{customLabel || title}</p>
            {loading ? (
              <div className="h-6 w-24 animate-pulse rounded bg-muted/50" />
            ) : (
              <div
                className={cn(fontSize, "font-bold tracking-tight cursor-default tabular-nums text-foreground/90")}
                title={rawValue !== undefined ? rawValue.toLocaleString('zh-CN') : undefined}
              >
                {value}
              </div>
            )}
          </div>
          <div className={cn("p-2 rounded-xl flex-shrink-0 transition-transform duration-300 group-hover:scale-110 shadow-sm relative z-10", theme.bg)}>
            <Icon className="w-4 h-4" />
          </div>
        </CardContent>
      </Card>
    )
  }

  return (
    <Card className="glass-card overflow-hidden hover:shadow-lg hover:-translate-y-1 transition-all duration-300 group">
      <CardContent className="p-5 relative overflow-hidden">
        <div className={cn("absolute -right-6 -top-6 w-24 h-24 rounded-full opacity-10 group-hover:opacity-20 transition-opacity duration-300 blur-2xl", theme.bg.split(' ')[0])} />
        <div className="flex justify-between items-start relative z-10">
          <div className="space-y-2">
            <p className="text-sm font-medium text-muted-foreground">{title}</p>
            {loading ? (
              <div className="h-8 w-20 animate-pulse rounded bg-muted/50" />
            ) : (
              <div className="text-2xl font-bold tracking-tight text-foreground/90">{value.toLocaleString()}</div>
            )}
          </div>
          <div className={cn("p-3 rounded-2xl transition-all duration-300 group-hover:scale-110 shadow-sm", theme.bg)}>
            <Icon className="w-5 h-5" />
          </div>
        </div>
        {loading ? (
          <div className="mt-4 h-6 w-28 animate-pulse rounded-full bg-muted/40" />
        ) : subValue && (
          <div className="mt-4 flex items-center text-xs relative z-10">
            <span className={cn("font-medium px-2.5 py-1 rounded-full bg-secondary/80 backdrop-blur-sm shadow-sm border border-black/5 dark:border-white/5", theme.text)}>
              {subValue}
            </span>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

interface KingCardProps {
  title: string
  subtitle: string
  icon: React.ElementType
  user: { user_id: number; username: string } | null | undefined
  valueLabel: string
  value: string | undefined
  gradient: string
  accentColor: string
  loading?: boolean
}

function KingCard({ title, subtitle, icon: Icon, user, valueLabel, value, gradient, accentColor, loading = false }: KingCardProps) {
  return (
    <div className={`glass-card bg-gradient-to-br ${gradient} rounded-2xl shadow-lg p-6 text-white relative overflow-hidden group hover:shadow-xl hover:-translate-y-1 transition-all duration-300 border border-white/20`}>
      {/* Background Pattern */}
      <div className="absolute top-0 right-0 -mr-4 -mt-4 opacity-10 group-hover:opacity-20 group-hover:scale-110 transition-all duration-500">
        <Icon className="w-32 h-32 rotate-12" />
      </div>

      <div className="flex items-center justify-between relative z-10">
        <div>
          <div className="flex items-center gap-2">
            <Icon className="w-5 h-5 opacity-90" />
            <p className="text-lg font-bold tracking-wide">{title}</p>
          </div>
          <p className={`text-sm mt-1 ${accentColor} opacity-90`}>{subtitle}</p>
        </div>
      </div>

      {loading ? (
        <div className="mt-6 h-[172px] animate-pulse rounded-lg border border-white/10 bg-white/10" />
      ) : user ? (
        <div className="mt-6 relative z-10">
          <div className="flex items-center bg-white/10 p-4 rounded-lg backdrop-blur-sm border border-white/10">
            <div className="h-12 w-12 rounded-full bg-white text-blue-600 flex items-center justify-center text-xl font-bold shadow-sm">
              {user.username.charAt(0).toUpperCase()}
            </div>
            <div className="ml-4">
              <p className="text-xl font-bold">{user.username}</p>
              <p className={`text-xs ${accentColor}`}>User ID: {user.user_id}</p>
            </div>
          </div>
          <div className="mt-4 flex justify-between items-end">
            <div>
              <p className={`text-xs ${accentColor} mb-1`}>{valueLabel}</p>
              <p className="text-3xl font-bold tracking-tight">{value}</p>
            </div>
          </div>
        </div>
      ) : (
        <div className="mt-6 h-[108px] flex flex-col items-center justify-center bg-white/5 rounded-lg border border-white/10 backdrop-blur-sm relative z-10">
          <p className="text-white/60">暂无数据</p>
        </div>
      )}
    </div>
  )
}
