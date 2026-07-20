export const WEEK_COLORS = ['#64748b', '#f59e0b', '#10b981', '#2563eb']
export const WEEKDAY_LABELS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

export function mondayIndex(date: Date): number {
  const day = date.getDay()
  return day === 0 ? 6 : day - 1
}

export function weekStartKey(date: Date): number {
  const start = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  start.setDate(start.getDate() - mondayIndex(start))
  return start.getTime()
}

export function weekRangeLabel(mondayMs: number): string {
  const monday = new Date(mondayMs)
  const sunday = new Date(mondayMs)
  sunday.setDate(sunday.getDate() + 6)
  const pad = (value: number) => String(value).padStart(2, '0')
  const format = (date: Date) => `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
  return `${format(monday)} ~ ${format(sunday)}`
}

export function relativeWeekLabel(index: number, total: number): string {
  const diff = total - 1 - index
  if (diff <= 0) return '本周'
  return `${diff}周前`
}

export function relativeWeekLabelFromDate(mondayMs: number, reference = new Date()): string {
  const diff = Math.round((weekStartKey(reference) - mondayMs) / (7 * 86400000))
  if (diff <= 0) return '本周'
  return `${diff}周前`
}
