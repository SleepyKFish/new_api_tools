/**
 * 共享格式化工具
 *
 * 用途：在 Dashboard / SpendAnalytics 等组件之间统一 quota→¥ 与 token 的格式。
 * 与 new-api 后端约定一致，符号统一为 `¥`。
 */

// 1 quota = 1/500000 元（与 new-api 后端约定一致，见 backend/internal/service/dashboard.go 中的 cost 计算）
export const QUOTA_PER_YUAN = 500_000

/** 把 quota 整数格式化为「¥xx」整数显示（不带小数）。用于 KPI/卡片。 */
export function formatCost(quota: number): string {
  return `¥${Math.round(quota / QUOTA_PER_YUAN)}`
}

/** 把 quota 格式化为「¥xx.xx」精确两位小数。用于 tooltip / 详细面板。 */
export function formatCostPrecise(quota: number): string {
  return `¥${(quota / QUOTA_PER_YUAN).toFixed(2)}`
}

/** 把 token 整数格式化为 1k/1M 形式。1k=1000, 1M=1_000_000。 */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n)) return '0'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return `${Math.round(n)}`
}

/** 用 zh-CN 千分位格式化普通整数。 */
export function formatNumber(num: number): string {
  return num.toLocaleString('zh-CN')
}

/** 格式化百分比变化（带正负号 + 箭头）。后端 changeRate 返回 0-100 浮点。 */
export function formatChangeRate(rate: number | null | undefined): {
  text: string
  positive: boolean
  display: string
} {
  if (rate === null || rate === undefined || !Number.isFinite(rate)) {
    return { text: '—', positive: true, display: '—' }
  }
  const positive = rate >= 0
  const display = `${positive ? '+' : ''}${rate.toFixed(1)}%`
  return { text: display, positive, display }
}

/** 截断长字符串并加省略号。 */
export function truncate(s: string, maxLen = 18): string {
  if (!s) return ''
  return s.length > maxLen ? `${s.slice(0, maxLen)}…` : s
}