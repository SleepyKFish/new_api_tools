// ============================================================
// Mock data for Dashboard development without backend
// ============================================================

interface MockTrend {
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

const now = new Date()

function pad(n: number) { return n < 10 ? `0${n}` : `${n}` }

// Deterministic-ish token generation from requests + seed
function genTokens(requests: number, seed: number): {
  prompt_tokens: number
  completion_tokens: number
  cache_hit_tokens: number
  cache_write_tokens: number
} {
  const r = (Math.sin(seed * 12.9898) * 43758.5453) % 1
  const promptPerReq = 200 + Math.floor(Math.abs(r) * 400) // 200-600
  const completionPerReq = 80 + Math.floor(Math.abs(Math.sin(seed * 7.3)) * 220) // 80-300
  const prompt_tokens = requests * promptPerReq
  const completion_tokens = requests * completionPerReq
  const cacheHitRate = 0.25 + Math.abs(Math.sin(seed * 3.7)) * 0.3 // 25-55%
  const cacheWriteRate = 0.05 + Math.abs(Math.cos(seed * 5.1)) * 0.1 // 5-15%
  const cache_hit_tokens = Math.floor(prompt_tokens * cacheHitRate)
  const cache_write_tokens = Math.floor(prompt_tokens * cacheWriteRate)
  return { prompt_tokens, completion_tokens, cache_hit_tokens, cache_write_tokens }
}

// Generate daily trend data for N days back from today
function genDaily(days: number, seed: number): MockTrend[] {
  const data: MockTrend[] = []
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now)
    d.setDate(d.getDate() - i)
    // Simulate weekly pattern: workday high, weekend low
    const dayOfWeek = d.getDay() // 0=Sun, 6=Sat
    const isWeekend = dayOfWeek === 0 || dayOfWeek === 6
    const base = isWeekend ? 200 + seed * 50 : 800 + seed * 100
    // Add some random variation
    const noise = Math.floor((Math.sin(i * 1.7 + seed * 3.1) * 0.3 + 0.5) * base * 0.4)
    const requests = base + noise
    const users = Math.floor(requests * (isWeekend ? 0.08 : 0.12) + Math.random() * 5)
    const quota = Math.floor(requests * 350 + Math.random() * 50000)
    const tokens = genTokens(requests, i * 13 + seed * 7)
    data.push({
      date: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`,
      timestamp: Math.floor(d.getTime() / 1000),
      request_count: requests,
      quota_used: quota,
      unique_users: users,
      ...tokens,
    })
  }
  return data
}

// Generate hourly trend data for 24h
function genHourly(seed: number): MockTrend[] {
  const data: MockTrend[] = []
  for (let i = 23; i >= 0; i--) {
    const d = new Date(now)
    d.setHours(d.getHours() - i, 0, 0, 0)
    const hour = d.getHours()
    // Simulate daily pattern: peak at 10am and 3pm, low at 3am
    const peakFactor = Math.max(0.1, 1 - Math.abs(hour - 10) / 12) * 0.6 + Math.max(0.1, 1 - Math.abs(hour - 15) / 12) * 0.5
    const base = Math.floor(peakFactor * 80 + 5)
    const noise = Math.floor(Math.random() * 30)
    const requests = base + noise
    const tokens = genTokens(requests, i * 11 + seed * 3)
    data.push({
      hour: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(hour)}:00`,
      timestamp: Math.floor(d.getTime() / 1000),
      request_count: requests,
      quota_used: Math.floor(requests * 350),
      ...tokens,
    })
  }
  return data
}

// Generate comparison: current + previous (offset by compareDays) + change rates
// compareDays: offset days for comparison (7=week, 30=month)
function genCompare(days: number, _compareDays: number): {
  current: MockTrend[]
  previous: MockTrend[]
  comparison: Array<{
    date: string
    request_count_change: number | null
    quota_used_change: number | null
    unique_users_change: number | null
  }>
} {
  const current = genDaily(days, 0)
  const previous = genDaily(days, 1) // slightly different seed for "last period"

  // Adjust previous to show a mild growth trend (current slightly higher)
  for (let i = 0; i < days; i++) {
    // Monday/Wednesday grow, weekend shrink a bit
    const d = new Date(now)
    d.setDate(d.getDate() - (days - 1 - i))
    const dow = d.getDay()
    const growthFactor = (dow >= 1 && dow <= 5) ? 0.85 + Math.random() * 0.1 : 1.05 + Math.random() * 0.1
    previous[i].request_count = Math.floor(current[i].request_count * growthFactor)
    previous[i].quota_used = Math.floor(current[i].quota_used * growthFactor * (0.9 + Math.random() * 0.2))
    previous[i].unique_users = Math.floor((current[i].unique_users ?? 0) * growthFactor)
    // Regenerate token data for previous period to match adjusted requests
    const prevTokens = genTokens(previous[i].request_count, i * 17 + 99)
    previous[i].prompt_tokens = prevTokens.prompt_tokens
    previous[i].completion_tokens = prevTokens.completion_tokens
    previous[i].cache_hit_tokens = prevTokens.cache_hit_tokens
    previous[i].cache_write_tokens = prevTokens.cache_write_tokens
  }

  const comparison = current.map((cur, i) => {
    const prev = previous[i]
    const reqChange = prev.request_count > 0
      ? Math.round(((cur.request_count - prev.request_count) / prev.request_count) * 10000) / 100
      : null
    const quotaChange = prev.quota_used > 0
      ? Math.round(((cur.quota_used - prev.quota_used) / prev.quota_used) * 10000) / 100
      : null
    const userChange = prev.unique_users && prev.unique_users > 0
      ? Math.round((((cur.unique_users || 0) - (prev.unique_users || 0)) / (prev.unique_users || 1)) * 10000) / 100
      : null
    return {
      date: cur.date!,
      request_count_change: reqChange,
      quota_used_change: quotaChange,
      unique_users_change: userChange,
    }
  })

  return { current, previous, comparison }
}

export const mockDashboardData = {
  overview: {
    total_users: 128,
    active_users: 47,
    total_tokens: 312,
    active_tokens: 203,
    total_channels: 15,
    active_channels: 12,
    total_models: 56,
    total_redemptions: 89,
    unused_redemptions: 23,
  },

  usage: {
    period: '7d',
    total_requests: 38421,
    total_quota_used: 18765432,
    total_prompt_tokens: 4523100,
    total_completion_tokens: 1897600,
    average_response_time: 1.234,
  },

  models: [
    { model_name: 'gpt-4o', request_count: 8450, quota_used: 5230000, prompt_tokens: 1200000, completion_tokens: 520000 },
    { model_name: 'claude-sonnet-4-20250514', request_count: 7200, quota_used: 4100000, prompt_tokens: 980000, completion_tokens: 430000 },
    { model_name: 'deepseek-chat', request_count: 6800, quota_used: 1200000, prompt_tokens: 750000, completion_tokens: 310000 },
    { model_name: 'gpt-4o-mini', request_count: 5500, quota_used: 890000, prompt_tokens: 480000, completion_tokens: 200000 },
    { model_name: 'gemini-2.5-flash', request_count: 4200, quota_used: 780000, prompt_tokens: 350000, completion_tokens: 150000 },
    { model_name: 'qwen-plus', request_count: 3100, quota_used: 290000, prompt_tokens: 280000, completion_tokens: 110000 },
    { model_name: 'claude-opus-4-20250514', request_count: 1800, quota_used: 2100000, prompt_tokens: 190000, completion_tokens: 85000 },
    { model_name: 'hunyuan-turbo', request_count: 1371, quota_used: 150000, prompt_tokens: 120000, completion_tokens: 52000 },
  ],

  topUsers: [
    { user_id: 1, username: 'dev-team-lead', request_count: 5200, quota_used: 3200000 },
    { user_id: 2, username: 'ai-app-backend', request_count: 4100, quota_used: 2800000 },
    { user_id: 3, username: 'chatbot-service', request_count: 3800, quota_used: 1900000 },
    { user_id: 4, username: 'data-pipeline', request_count: 2900, quota_used: 1600000 },
    { user_id: 5, username: 'frontend-app', request_count: 2500, quota_used: 1200000 },
  ],

  // Daily trends: current
  getDaily7d() { return genDaily(7, 0) },

  // Daily trends: week-over-week comparison
  getDaily7dWeekCompare() { return genCompare(7, 7) },

  // Daily trends: month-over-month comparison
  getDaily7dMonthCompare() { return genCompare(7, 30) },

  // Daily trends: 3-day
  getDaily3d() { return genDaily(3, 0) },

  // Daily trends: 14-day
  getDaily14d() { return genDaily(14, 0) },

  // Hourly trends
  getHourly24h() { return genHourly(0) },

  // Hourly trends: day-over-day comparison
  getHourly24hCompare() {
    const current = genHourly(0)
    const previous = genHourly(1)
    // Adjust previous to show mild growth
    for (let i = 0; i < 24; i++) {
      previous[i].request_count = Math.floor(current[i].request_count * (0.8 + Math.random() * 0.3))
      previous[i].quota_used = Math.floor(current[i].quota_used * (0.8 + Math.random() * 0.3))
      const prevTokens = genTokens(previous[i].request_count, i * 17 + 99)
      previous[i].prompt_tokens = prevTokens.prompt_tokens
      previous[i].completion_tokens = prevTokens.completion_tokens
      previous[i].cache_hit_tokens = prevTokens.cache_hit_tokens
      previous[i].cache_write_tokens = prevTokens.cache_write_tokens
    }
    const comparison = current.map((cur, i) => {
      const prev = previous[i]
      const reqChange = prev.request_count > 0
        ? Math.round(((cur.request_count - prev.request_count) / prev.request_count) * 10000) / 100
        : null
      const quotaChange = prev.quota_used > 0
        ? Math.round(((cur.quota_used - prev.quota_used) / prev.quota_used) * 10000) / 100
        : null
      return { hour: cur.hour, request_count_change: reqChange, quota_used_change: quotaChange, unique_users_change: undefined }
    })
    return { current, previous, comparison }
  },

  // ===== New period-based generators =====

  // Today: hourly data from 00:00 to current hour
  getToday() {
    const currentHour = now.getHours()
    const data: MockTrend[] = []
    for (let h = 0; h <= currentHour; h++) {
      const d = new Date(now)
      d.setHours(h, 0, 0, 0)
      const peakFactor = Math.max(0.1, 1 - Math.abs(h - 10) / 12) * 0.6 + Math.max(0.1, 1 - Math.abs(h - 15) / 12) * 0.5
      const base = Math.floor(peakFactor * 80 + 5)
      const noise = Math.floor(Math.random() * 30)
      const requests = base + noise
      const tokens = genTokens(requests, h * 11)
      data.push({
        hour: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(h)}:00`,
        timestamp: Math.floor(d.getTime() / 1000),
        request_count: requests,
        quota_used: Math.floor(requests * 350),
        ...tokens,
      })
    }
    return data
  },

  // This week: daily data from Monday to today
  getThisWeek() {
    const dayOfWeek = now.getDay() // 0=Sun
    const mondayOffset = dayOfWeek === 0 ? 6 : dayOfWeek - 1 // days since Monday
    const days = mondayOffset + 1 // Mon..today
    const data: MockTrend[] = []
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(now)
      d.setDate(d.getDate() - i)
      const dow = d.getDay()
      const isWeekend = dow === 0 || dow === 6
      const base = isWeekend ? 200 : 800
      const noise = Math.floor((Math.sin(i * 1.7) * 0.3 + 0.5) * base * 0.4)
      const requests = base + noise
      const users = Math.floor(requests * (isWeekend ? 0.08 : 0.12) + Math.random() * 5)
      const quota = Math.floor(requests * 350 + Math.random() * 50000)
      const tokens = genTokens(requests, i * 13)
      data.push({
        date: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`,
        timestamp: Math.floor(d.getTime() / 1000),
        request_count: requests,
        quota_used: quota,
        unique_users: users,
        ...tokens,
      })
    }
    return data
  },

  // Multi-week daily history (N days back from today), used by 周同比 / 周花费 视图。
  // 复用 genDaily 的工作日/周末花费模式,含每日 quota 与输入/输出/缓存拆分。
  getDailyHistory(days: number) { return genDaily(days, 0) },

  // This month: daily data from 1st to today
  getThisMonth() {
    const today = now.getDate()
    const data: MockTrend[] = []
    for (let i = today - 1; i >= 0; i--) {
      const d = new Date(now)
      d.setDate(now.getDate() - i)
      const dow = d.getDay()
      const isWeekend = dow === 0 || dow === 6
      const base = isWeekend ? 200 : 800
      const noise = Math.floor((Math.sin(i * 1.7) * 0.3 + 0.5) * base * 0.4)
      const requests = base + noise
      const users = Math.floor(requests * (isWeekend ? 0.08 : 0.12) + Math.random() * 5)
      const quota = Math.floor(requests * 350 + Math.random() * 50000)
      const tokens = genTokens(requests, i * 13)
      data.push({
        date: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`,
        timestamp: Math.floor(d.getTime() / 1000),
        request_count: requests,
        quota_used: quota,
        unique_users: users,
        ...tokens,
      })
    }
    return data
  },
}

// ============================================================
// Model monitoring mock data
// ============================================================

const MODEL_NAMES = [
  'gpt-4o', 'gpt-4o-mini', 'claude-sonnet-4-20250514', 'claude-opus-4-20250514',
  'deepseek-chat', 'deepseek-reasoner', 'gemini-2.5-flash', 'gemini-2.5-pro',
  'qwen-plus', 'qwen-max', 'hunyuan-turbo', 'glm-4',
]

interface MockPerformanceRange {
  startTime: number
  endTime: number
}

function getMockTimeConfig(window: string, range?: MockPerformanceRange) {
  const windowSeconds: Record<string, number> = {
    '15m': 15 * 60,
    '30m': 30 * 60,
    '1h': 60 * 60,
    '6h': 6 * 60 * 60,
    '12h': 12 * 60 * 60,
    '24h': 24 * 60 * 60,
  }
  const customMatch = window.match(/^(\d+)(?:min|m|h)$/)
  const customSeconds = customMatch
    ? Number(customMatch[1]) * (window.endsWith('h') ? 60 * 60 : 60)
    : 0
  const totalSeconds = range
    ? Math.max(60, range.endTime - range.startTime)
    : windowSeconds[window] || customSeconds || windowSeconds['24h']
  const slotCount = totalSeconds <= 60 * 60
    ? Math.max(1, Math.floor(totalSeconds / 60))
    : 24
  const slotSeconds = Math.max(60, Math.floor(totalSeconds / slotCount))
  const endTime = range?.endTime ?? Math.floor(Date.now() / 1000)
  const startTime = range?.startTime ?? endTime - totalSeconds
  return { startTime, endTime, slotCount, slotSeconds }
}

function genSlotData(window: string, range?: MockPerformanceRange): Array<{
  slot: number; start_time: number; end_time: number
  total_requests: number; success_count: number; failure_count: number
  format_error_count: number; rate_limit_count: number; empty_count: number
  non_format_failure_count: number; model_failure_count: number; model_error_count: number
  non_empty_count: number; success_rate: number; model_success_rate: number; model_availability_rate: number
  status: string; model_status: string
  within_5s_rate: number | null; within_10s_rate: number | null
  duration_within_10s_rate: number | null; duration_within_20s_rate: number | null
  cache_hit_rate: number | null; cache_write_rate: number | null
  cache_hit_tokens: number; cache_write_tokens: number
  total_input_tokens: number; total_output_tokens: number
  total_quota: number
  completion_tps: number | null; timed_requests: number; duration_timed_requests: number; output_requests: number
}> {
  const slots: ReturnType<typeof genSlotData> = []
  const { startTime, endTime, slotCount, slotSeconds } = getMockTimeConfig(window, range)

  for (let i = 0; i < slotCount; i++) {
    const slotStart = startTime + i * slotSeconds
    const slotEnd = i === slotCount - 1 ? endTime : slotStart + slotSeconds
    const hour = new Date(slotStart * 1000).getHours()
    // Simulate daily pattern
    const peak = Math.max(0.15, 1 - Math.abs(hour - 11) / 12) * 0.7 + Math.max(0.1, 1 - Math.abs(hour - 16) / 12) * 0.4
    const req = Math.floor((30 + Math.random() * 20) * peak * (0.7 + Math.random() * 0.6))
    const success = Math.floor(req * (0.88 + Math.random() * 0.1))
    const fail = req - success - Math.floor(Math.random() * 2)
    const total = success + fail + (Math.random() > 0.9 ? Math.floor(Math.random() * 2) : 0)
    const inputTk = Math.floor(total * (200 + Math.random() * 800))
    const outputTk = Math.floor(total * (50 + Math.random() * 400))
    const quota = Math.floor((inputTk + outputTk) * (0.3 + Math.random() * 0.5))

    slots.push({
      slot: i,
      start_time: slotStart,
      end_time: slotEnd,
      total_requests: total,
      success_count: success,
      failure_count: fail,
      format_error_count: Math.floor(fail * 0.4),
      rate_limit_count: Math.floor(fail * 0.1),
      empty_count: Math.floor(Math.random() * 2),
      non_format_failure_count: Math.floor(fail * 0.5),
      model_failure_count: Math.floor(fail * 0.5),
      model_error_count: Math.floor(fail * 0.5) + Math.floor(Math.random() * 2),
      non_empty_count: success,
      success_rate: total > 0 ? success / total * 100 : 100,
      model_success_rate: total > 0 ? (success + fail) / total * 100 : 100,
      model_availability_rate: total > 0 ? (success + fail) / total * 100 : 100,
      status: (total > 0 && success / total >= 0.95 ? 'green' : total > 0 && success / total >= 0.8 ? 'yellow' : 'red') as 'green' | 'yellow' | 'red',
      model_status: (total > 0 && (success + fail) / total >= 0.95 ? 'green' : 'yellow') as 'green' | 'yellow' | 'red',
      within_5s_rate: 50 + Math.random() * 40,
      within_10s_rate: 70 + Math.random() * 25,
      duration_within_10s_rate: 60 + Math.random() * 30,
      duration_within_20s_rate: 80 + Math.random() * 15,
      cache_hit_rate: Math.random() > 0.5 ? Math.random() * 60 : null,
      cache_write_rate: Math.random() > 0.7 ? Math.random() * 30 : null,
      cache_hit_tokens: Math.floor(inputTk * (Math.random() * 0.4)),
      cache_write_tokens: Math.floor(inputTk * (Math.random() * 0.1)),
      total_input_tokens: inputTk,
      total_output_tokens: outputTk,
      total_quota: quota,
      completion_tps: Math.random() * 80 + 20,
      timed_requests: success,
      duration_timed_requests: total,
      output_requests: Math.floor(outputTk / 50),
    })
  }
  return slots
}

function genModelStatus(name: string, window: string, range?: MockPerformanceRange) {
  const slots = genSlotData(window, range)
  const totalReq = slots.reduce((s, sl) => s + sl.total_requests, 0)
  const totalSuccess = slots.reduce((s, sl) => s + sl.success_count, 0)
  const totalFail = slots.reduce((s, sl) => s + sl.failure_count, 0)
  const totalEmpty = slots.reduce((s, sl) => s + sl.empty_count, 0)
  const totalFmt = slots.reduce((s, sl) => s + sl.format_error_count, 0)
  const totalRateLim = slots.reduce((s, sl) => s + sl.rate_limit_count, 0)
  const inputTk = slots.reduce((s, sl) => s + sl.total_input_tokens, 0)
  const outputTk = slots.reduce((s, sl) => s + sl.total_output_tokens, 0)
  const quota = slots.reduce((s, sl) => s + sl.total_quota, 0)

  return {
    model_name: name, display_name: name, time_window: window,
    total_requests: totalReq,
    success_count: totalSuccess, failure_count: totalFail,
    format_error_count: totalFmt, rate_limit_count: totalRateLim,
    non_format_failure_count: Math.floor(totalFail * 0.5),
    model_failure_count: Math.floor(totalFail * 0.5),
    model_error_count: Math.floor(totalFail * 0.5) + totalEmpty,
    non_empty_count: totalSuccess, empty_count: totalEmpty,
    success_rate: totalReq > 0 ? Math.round(totalSuccess / totalReq * 10000) / 100 : 100,
    model_success_rate: totalReq > 0 ? Math.round((totalSuccess + totalFail) / totalReq * 10000) / 100 : 100,
    model_availability_rate: totalReq > 0 ? Math.round((totalSuccess + totalFail) / totalReq * 10000) / 100 : 100,
    current_status: (totalReq > 0 && totalSuccess / totalReq >= 0.95 ? 'green' : totalReq > 0 && totalSuccess / totalReq >= 0.8 ? 'yellow' : 'red') as 'green' | 'yellow' | 'red',
    model_current_status: 'green' as 'green' | 'yellow' | 'red',
    within_5s_rate: 55 + Math.random() * 35,
    within_10s_rate: 75 + Math.random() * 20,
    duration_within_10s_rate: 65 + Math.random() * 25,
    duration_within_20s_rate: 85 + Math.random() * 10,
    cache_hit_rate: Math.random() > 0.4 ? Math.random() * 50 : null,
    cache_write_rate: Math.random() > 0.6 ? Math.random() * 20 : null,
    cache_hit_tokens: Math.floor(inputTk * 0.2),
    cache_write_tokens: Math.floor(inputTk * 0.05),
    total_input_tokens: inputTk,
    total_output_tokens: outputTk,
    total_quota: quota,
    completion_tps: Math.random() * 60 + 30,
    timed_requests: totalSuccess,
    duration_timed_requests: totalReq,
    output_requests: Math.floor(outputTk / 50),
    slot_data: slots,
  }
}

function genChannelPerformance(id: number, name: string, window: string, modelCount: number, range?: MockPerformanceRange) {
  const slots = genSlotData(window, range)
  const totalReq = slots.reduce((s, sl) => s + sl.total_requests, 0)
  const totalSuccess = slots.reduce((s, sl) => s + sl.success_count, 0)
  const totalFail = slots.reduce((s, sl) => s + sl.failure_count, 0)
  const totalEmpty = slots.reduce((s, sl) => s + sl.empty_count, 0)
  const inputTk = slots.reduce((s, sl) => s + sl.total_input_tokens, 0)
  const outputTk = slots.reduce((s, sl) => s + sl.total_output_tokens, 0)
  const quota = slots.reduce((s, sl) => s + sl.total_quota, 0)

  return {
    channel_id: id, channel_name: name, model_count: modelCount,
    total_requests: totalReq,
    success_count: totalSuccess, failure_count: totalFail,
    format_error_count: Math.floor(totalFail * 0.4),
    rate_limit_count: Math.floor(totalFail * 0.1),
    non_format_failure_count: Math.floor(totalFail * 0.5),
    model_failure_count: Math.floor(totalFail * 0.5),
    model_error_count: Math.floor(totalFail * 0.5) + totalEmpty,
    non_empty_count: totalSuccess, empty_count: totalEmpty,
    success_rate: totalReq > 0 ? Math.round(totalSuccess / totalReq * 10000) / 100 : 100,
    model_success_rate: totalReq > 0 ? Math.round((totalSuccess + totalFail) / totalReq * 10000) / 100 : 100,
    model_availability_rate: totalReq > 0 ? Math.round((totalSuccess + totalFail) / totalReq * 10000) / 100 : 100,
    current_status: 'green' as 'green' | 'yellow' | 'red',
    model_current_status: 'green' as 'green' | 'yellow' | 'red',
    within_5s_rate: 50 + Math.random() * 40,
    within_10s_rate: 70 + Math.random() * 25,
    duration_within_10s_rate: 60 + Math.random() * 30,
    duration_within_20s_rate: 80 + Math.random() * 15,
    cache_hit_rate: Math.random() > 0.4 ? Math.random() * 50 : null,
    cache_write_rate: Math.random() > 0.6 ? Math.random() * 20 : null,
    cache_hit_tokens: Math.floor(inputTk * 0.2),
    cache_write_tokens: Math.floor(inputTk * 0.05),
    total_input_tokens: inputTk,
    total_output_tokens: outputTk,
    total_quota: quota,
    completion_tps: Math.random() * 60 + 30,
    timed_requests: totalSuccess,
    duration_timed_requests: totalReq,
    output_requests: Math.floor(outputTk / 50),
    slot_data: slots,
  }
}

const CHANNELS = [
  { id: 1, name: 'OpenAI Primary', modelCount: 4 },
  { id: 2, name: 'Anthropic Direct', modelCount: 2 },
  { id: 3, name: 'DeepSeek CN', modelCount: 2 },
  { id: 5, name: 'Google Gemini', modelCount: 2 },
  { id: 7, name: 'Aliyun Bailian', modelCount: 2 },
]

export const mockModelStatus = {
  getAvailableModels() {
    return MODEL_NAMES.map(name => ({
      model_name: name,
      request_count_24h: Math.floor(500 + Math.random() * 8000),
    }))
  },

  getPerformanceSummary(window = '24h', range?: MockPerformanceRange) {
    const queryKey = range ? `range:${range.startTime}:${range.endTime}` : window
    const models = MODEL_NAMES.map(name => genModelStatus(name, queryKey, range))
    const channels = CHANNELS.map(ch => genChannelPerformance(ch.id, ch.name, queryKey, ch.modelCount, range))
    return {
      models,
      channels,
      time_window: queryKey,
      start_time: range?.startTime,
      end_time: range?.endTime,
    }
  },

  getChannelModelPerformance(channelId: number, window = '24h', range?: MockPerformanceRange) {
    const ch = CHANNELS.find(c => c.id === channelId)
    const modelCount = ch?.modelCount ?? 2
    const queryKey = range ? `range:${range.startTime}:${range.endTime}` : window
    const models = MODEL_NAMES.slice(0, modelCount).map(name => genModelStatus(name, queryKey, range))
    return {
      channel_id: channelId,
      channel_name: ch?.name ?? `Channel#${channelId}`,
      window: queryKey, time_window: queryKey,
      start_time: range?.startTime, end_time: range?.endTime,
      total: models.length, limit: 100, offset: 0, has_more: false,
      data: models.map(m => ({ ...m, channel_id: channelId, channel_name: ch?.name ?? '' })),
      success: true,
    }
  },

  getTokenGroups() {
    return [
      { group_name: 'default', model_count: 12, models: MODEL_NAMES },
    ]
  },

  getChannelCostTrends(days: number, compareMode: string) {
    const today = new Date()
    const pad = (n: number) => n < 10 ? `0${n}` : ''
    const fmt = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}`

    const genCostData = (baseQuota: number, seed: number) => {
      const data: Array<{date: string; total_quota: number; total_tokens: number; total_requests: number}> = []
      for (let i = days - 1; i >= 0; i--) {
        const d = new Date(today)
        d.setDate(d.getDate() - i)
        const dow = d.getDay()
        const isWeekend = dow === 0 || dow === 6
        const noise = Math.sin(i * 1.3 + seed) * 0.3 + 1
        const quota = Math.floor(baseQuota * (isWeekend ? 0.5 : 1) * noise)
        const tokens = Math.floor(quota / 3.5)
        const requests = Math.floor(quota / 500 + Math.random() * 200)
        data.push({ date: fmt(d), total_quota: quota, total_tokens: tokens, total_requests: requests })
      }
      return data
    }

    const channels = [
      { id: 1, name: 'OpenAI Primary', base: 2800000 },
      { id: 2, name: 'Anthropic Direct', base: 2100000 },
      { id: 3, name: 'DeepSeek CN', base: 850000 },
      { id: 5, name: 'Google Gemini', base: 620000 },
      { id: 7, name: 'Aliyun Bailian', base: 350000 },
    ]

    if (!compareMode) {
      return {
        channels: channels.map(ch => ({
          channel_id: ch.id,
          channel_name: ch.name,
          current: genCostData(ch.base, ch.id),
        })),
      }
    }

    const offsetDays = compareMode === 'month' ? 30 : 7
    return {
      channels: channels.map(ch => {
        const current = genCostData(ch.base, ch.id)
        const previous = genCostData(ch.base * 0.88, ch.id + 10)
        const comparison = current.map((cur, i) => {
          const prev = previous[i]
          const change = prev.total_quota > 0
            ? Math.round(((cur.total_quota - prev.total_quota) / prev.total_quota) * 10000) / 100
            : null
          return { date: cur.date, total_quota_change: change }
        })
        return {
          channel_id: ch.id,
          channel_name: ch.name,
          current,
          previous,
          comparison,
        }
      }),
      compare_mode: compareMode === 'month' ? 'month_over_month' : 'week_over_week',
      compare_offset: offsetDays,
    }
  },
}
