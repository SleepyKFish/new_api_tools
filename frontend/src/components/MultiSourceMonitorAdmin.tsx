import { useState, useEffect, useCallback, useMemo } from 'react'
import { useAuth } from '../contexts/AuthContext'
import { useToast } from './Toast'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Select } from './ui/select'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import {
  Plus, Trash2, Server, Globe, RefreshCw, ExternalLink, Save, Search,
  Loader2, AlertTriangle, ChevronDown, ChevronUp, SlidersHorizontal,
} from 'lucide-react'
import { cn } from '../lib/utils'

// ============================================================================
// 类型
// ============================================================================

interface MonitorSource {
  id: string
  name: string
  type: 'local' | 'remote_http'
  base_url: string
  enabled: boolean
  models: string[]
  token?: string
}

interface MultiSourceConfig {
  time_window: string
  refresh_interval: number
  title: string
  theme: string
  agg_cache_ttl: number
}

interface ErrorRuleConfig {
  rate_limit_status_codes: number[]
  user_format_status_codes: number[]
  user_format_min: number
  user_format_max: number
  rate_limit_keywords: string[]
  user_format_keywords: string[]
  count_rate_limit_as_model_error: boolean
}

interface AvailableModel {
  model_name: string
  request_count_24h?: number
}

// ============================================================================
// 常量
// ============================================================================

const apiUrl = import.meta.env.VITE_API_URL || ''
const WINDOW_PRESETS = ['15m', '30m', '1h', '6h', '12h', '24h']
const REFRESH_PRESETS = [30, 60, 120, 300]
const THEME_OPTIONS = [
  'daylight', 'obsidian', 'minimal', 'neon', 'forest', 'ocean', 'terminal',
  'cupertino', 'material', 'openai', 'anthropic', 'vercel', 'linear',
  'stripe', 'github', 'discord', 'tesla',
]

// ============================================================================
// 主组件
// ============================================================================

export function MultiSourceMonitorAdmin() {
  const { token } = useAuth()
  const { showToast } = useToast()

  const [sources, setSources] = useState<MonitorSource[]>([])
  const [config, setConfig] = useState<MultiSourceConfig | null>(null)
  const [rules, setRules] = useState<ErrorRuleConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [showRules, setShowRules] = useState(false)

  const authHeaders = useMemo((): Record<string, string> => ({
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  }), [token])

  const loadAll = useCallback(async () => {
    setLoading(true)
    try {
      const [sRes, cRes, rRes] = await Promise.all([
        fetch(`${apiUrl}/api/multi-source/sources`, { headers: authHeaders }),
        fetch(`${apiUrl}/api/multi-source/config`, { headers: authHeaders }),
        fetch(`${apiUrl}/api/multi-source/error-rules`, { headers: authHeaders }),
      ])
      const sJson = await sRes.json()
      const cJson = await cRes.json()
      const rJson = await rRes.json()
      if (sJson.success) setSources(sJson.data || [])
      if (cJson.success) setConfig(cJson.data)
      if (rJson.success) setRules(rJson.data)
    } catch {
      showToast('error', '加载配置失败')
    } finally {
      setLoading(false)
    }
  }, [authHeaders, showToast])

  useEffect(() => { loadAll() }, [loadAll])

  // ---- 源操作 ----
  const saveSource = useCallback(async (src: MonitorSource) => {
    const method = src.id ? 'PUT' : 'POST'
    const url = src.id
      ? `${apiUrl}/api/multi-source/sources/${src.id}`
      : `${apiUrl}/api/multi-source/sources`
    try {
      const res = await fetch(url, { method, headers: authHeaders, body: JSON.stringify(src) })
      const json = await res.json()
      if (!json.success) throw new Error(json.error?.message || '保存失败')
      showToast('success', '源已保存')
      await loadAll()
    } catch (e) {
      showToast('error', e instanceof Error ? e.message : '保存失败')
    }
  }, [authHeaders, showToast, loadAll])

  const deleteSource = useCallback(async (id: string) => {
    if (!confirm('确定删除该源？')) return
    try {
      const res = await fetch(`${apiUrl}/api/multi-source/sources/${id}`, { method: 'DELETE', headers: authHeaders })
      const json = await res.json()
      if (!json.success) throw new Error(json.error?.message || '删除失败')
      showToast('success', '源已删除')
      await loadAll()
    } catch (e) {
      showToast('error', e instanceof Error ? e.message : '删除失败')
    }
  }, [authHeaders, showToast, loadAll])

  const setSourceModels = useCallback(async (id: string, models: string[]) => {
    try {
      const res = await fetch(`${apiUrl}/api/multi-source/sources/${id}/models`, {
        method: 'PUT', headers: authHeaders, body: JSON.stringify({ models }),
      })
      const json = await res.json()
      if (!json.success) throw new Error(json.error?.message || '保存失败')
      setSources((prev) => prev.map((s) => (s.id === id ? { ...s, models } : s)))
      showToast('success', '模型选择已保存')
    } catch (e) {
      showToast('error', e instanceof Error ? e.message : '保存失败')
    }
  }, [authHeaders, showToast])

  const addRemoteSource = useCallback(() => {
    saveSource({ id: '', name: '新远程源', type: 'remote_http', base_url: '', enabled: true, models: [] })
  }, [saveSource])

  // ---- 配置保存 ----
  const saveConfig = useCallback(async (next: MultiSourceConfig) => {
    setConfig(next)
    try {
      const res = await fetch(`${apiUrl}/api/multi-source/config`, {
        method: 'PUT', headers: authHeaders, body: JSON.stringify(next),
      })
      const json = await res.json()
      if (!json.success) throw new Error('保存失败')
      if (json.data) setConfig(json.data)
      showToast('success', '配置已保存 (热更新生效)')
    } catch {
      showToast('error', '配置保存失败')
    }
  }, [authHeaders, showToast])

  const saveRules = useCallback(async (next: ErrorRuleConfig) => {
    setRules(next)
    try {
      const res = await fetch(`${apiUrl}/api/multi-source/error-rules`, {
        method: 'PUT', headers: authHeaders, body: JSON.stringify(next),
      })
      const json = await res.json()
      if (!json.success) throw new Error('保存失败')
      if (json.data) setRules(json.data)
      showToast('success', '错误分类规则已保存 (热更新生效)')
    } catch {
      showToast('error', '规则保存失败')
    }
  }, [authHeaders, showToast])

  if (loading) {
    return (
      <div className="flex items-center justify-center py-32">
        <Loader2 className="w-8 h-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* 标题栏 */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">多源模型监控</h1>
          <p className="text-sm text-muted-foreground mt-1">
            聚合本机与远程同款监控数据，公开展示。配置即时热更新。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={loadAll}>
            <RefreshCw className="w-4 h-4 mr-1.5" /> 刷新
          </Button>
          <Button variant="outline" size="sm" onClick={() => window.open('/multi.html', '_blank')}>
            <ExternalLink className="w-4 h-4 mr-1.5" /> 打开公开页
          </Button>
        </div>
      </div>

      {/* 全局配置 */}
      {config && <GlobalConfigCard config={config} onSave={saveConfig} />}

      {/* 错误分类规则 */}
      {rules && (
        <Card>
          <CardHeader className="cursor-pointer" onClick={() => setShowRules((v) => !v)}>
            <CardTitle className="flex items-center justify-between text-base">
              <span className="flex items-center gap-2">
                <SlidersHorizontal className="w-4 h-4" /> 错误分类规则
              </span>
              {showRules ? <ChevronUp className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
            </CardTitle>
          </CardHeader>
          {showRules && <ErrorRulesCard rules={rules} onSave={saveRules} />}
        </Card>
      )}

      {/* 源列表 */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-semibold">信息源</h2>
          <Button size="sm" onClick={addRemoteSource}>
            <Plus className="w-4 h-4 mr-1.5" /> 添加远程源
          </Button>
        </div>
        {sources.map((src) => (
          <SourceCard
            key={src.id}
            source={src}
            authHeaders={authHeaders}
            onSave={saveSource}
            onDelete={deleteSource}
            onSetModels={setSourceModels}
          />
        ))}
      </div>
    </div>
  )
}

// ============================================================================
// 全局配置卡片
// ============================================================================

function GlobalConfigCard({ config, onSave }: { config: MultiSourceConfig; onSave: (c: MultiSourceConfig) => void }) {
  const [local, setLocal] = useState(config)
  useEffect(() => setLocal(config), [config])

  const dirty = JSON.stringify(local) !== JSON.stringify(config)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">展示设置</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
          <Field label="公开页标题">
            <Input value={local.title} onChange={(e) => setLocal({ ...local, title: e.target.value })} placeholder="模型监控" />
          </Field>
          <Field label="时间窗口 (仅展示最近)">
            <Select value={local.time_window} onChange={(e) => setLocal({ ...local, time_window: e.target.value })}>
              {WINDOW_PRESETS.map((w) => <option key={w} value={w}>{w}</option>)}
              {!WINDOW_PRESETS.includes(local.time_window) && <option value={local.time_window}>{local.time_window}</option>}
            </Select>
            <Input
              className="mt-2"
              placeholder="自定义如 45min / 2h"
              defaultValue={WINDOW_PRESETS.includes(local.time_window) ? '' : local.time_window}
              onBlur={(e) => { if (e.target.value.trim()) setLocal({ ...local, time_window: e.target.value.trim() }) }}
            />
          </Field>
          <Field label="刷新间隔 (秒)">
            <Select value={String(local.refresh_interval)} onChange={(e) => setLocal({ ...local, refresh_interval: parseInt(e.target.value, 10) })}>
              {REFRESH_PRESETS.map((r) => <option key={r} value={r}>{r}s</option>)}
              {!REFRESH_PRESETS.includes(local.refresh_interval) && <option value={local.refresh_interval}>{local.refresh_interval}s</option>}
            </Select>
          </Field>
          <Field label="主题">
            <Select value={local.theme} onChange={(e) => setLocal({ ...local, theme: e.target.value })}>
              {THEME_OPTIONS.map((t) => <option key={t} value={t}>{t}</option>)}
            </Select>
          </Field>
        </div>
        <div className="flex justify-end">
          <Button size="sm" disabled={!dirty} onClick={() => onSave(local)}>
            <Save className="w-4 h-4 mr-1.5" /> 保存展示设置
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ============================================================================
// 错误规则卡片
// ============================================================================

function ErrorRulesCard({ rules, onSave }: { rules: ErrorRuleConfig; onSave: (r: ErrorRuleConfig) => void }) {
  const [local, setLocal] = useState(rules)
  useEffect(() => setLocal(rules), [rules])
  const dirty = JSON.stringify(local) !== JSON.stringify(rules)

  const numList = (v: string) => v.split(/[,，\s]+/).map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n))
  const strList = (v: string) => v.split(/[,，]+/).map((s) => s.trim()).filter(Boolean)

  return (
    <CardContent className="space-y-4">
      <p className="text-xs text-muted-foreground">
        先按 HTTP 状态码归类，状态码缺失时再用关键字兜底。限速默认计入「模型侧错误」(平台本身不做限速)。
      </p>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <Field label="限速状态码 (→ 限速)">
          <Input
            defaultValue={local.rate_limit_status_codes.join(', ')}
            onBlur={(e) => setLocal({ ...local, rate_limit_status_codes: numList(e.target.value) })}
            placeholder="429"
          />
        </Field>
        <Field label="格式错误状态码 (→ 用户格式错误)">
          <Input
            defaultValue={local.user_format_status_codes.join(', ')}
            onBlur={(e) => setLocal({ ...local, user_format_status_codes: numList(e.target.value) })}
            placeholder="400, 401, 403, 404, 422"
          />
        </Field>
        <Field label="格式错误状态码区间下限">
          <Input type="number" value={local.user_format_min}
            onChange={(e) => setLocal({ ...local, user_format_min: parseInt(e.target.value, 10) || 0 })} />
        </Field>
        <Field label="格式错误状态码区间上限">
          <Input type="number" value={local.user_format_max}
            onChange={(e) => setLocal({ ...local, user_format_max: parseInt(e.target.value, 10) || 0 })} />
        </Field>
        <Field label="限速关键字 (兜底匹配)">
          <Input
            defaultValue={local.rate_limit_keywords.join(', ')}
            onBlur={(e) => setLocal({ ...local, rate_limit_keywords: strList(e.target.value) })}
            placeholder="rate_limit, overloaded"
          />
        </Field>
        <Field label="格式错误关键字 (兜底匹配)">
          <Input
            defaultValue={local.user_format_keywords.join(', ')}
            onBlur={(e) => setLocal({ ...local, user_format_keywords: strList(e.target.value) })}
            placeholder="invalid_request, bad request"
          />
        </Field>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={local.count_rate_limit_as_model_error}
          onChange={(e) => setLocal({ ...local, count_rate_limit_as_model_error: e.target.checked })}
        />
        限速计入「模型侧错误」(参与成功率分母)
      </label>
      <div className="flex justify-end">
        <Button size="sm" disabled={!dirty} onClick={() => onSave(local)}>
          <Save className="w-4 h-4 mr-1.5" /> 保存规则
        </Button>
      </div>
    </CardContent>
  )
}

// ============================================================================
// 单个源卡片
// ============================================================================

function SourceCard({
  source, authHeaders, onSave, onDelete, onSetModels,
}: {
  source: MonitorSource
  authHeaders: Record<string, string>
  onSave: (s: MonitorSource) => void
  onDelete: (id: string) => void
  onSetModels: (id: string, models: string[]) => void
}) {
  const [local, setLocal] = useState(source)
  const [available, setAvailable] = useState<AvailableModel[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState(false)

  useEffect(() => setLocal(source), [source])

  const isLocal = source.type === 'local'
  const metaDirty =
    local.name !== source.name ||
    local.base_url !== source.base_url ||
    local.enabled !== source.enabled ||
    (local.token || '') !== (source.token || '')

  const loadModels = useCallback(async () => {
    setLoadingModels(true)
    setModelsError(null)
    try {
      const res = await fetch(`${apiUrl}/api/multi-source/sources/${source.id}/available-models`, { headers: authHeaders })
      const json = await res.json()
      if (!json.success) throw new Error(json.error?.message || '获取模型失败')
      setAvailable(json.data || [])
    } catch (e) {
      setModelsError(e instanceof Error ? e.message : '获取模型失败')
    } finally {
      setLoadingModels(false)
    }
  }, [authHeaders, source.id])

  useEffect(() => {
    if (expanded && available.length === 0 && !modelsError) loadModels()
  }, [expanded, available.length, modelsError, loadModels])

  const toggleModel = (name: string) => {
    const next = source.models.includes(name)
      ? source.models.filter((m) => m !== name)
      : [...source.models, name]
    onSetModels(source.id, next)
  }

  const filtered = available.filter((m) => m.model_name.toLowerCase().includes(search.toLowerCase()))

  return (
    <Card className={cn(!local.enabled && 'opacity-60')}>
      <CardContent className="pt-5 space-y-4">
        {/* 源元信息行 */}
        <div className="flex items-start gap-3 flex-wrap">
          <div className="flex items-center justify-center w-9 h-9 rounded-lg bg-primary/10 text-primary flex-shrink-0">
            {isLocal ? <Server className="w-5 h-5" /> : <Globe className="w-5 h-5" />}
          </div>
          <div className="flex-1 min-w-[200px] grid grid-cols-1 sm:grid-cols-2 gap-2">
            <Input
              value={local.name}
              onChange={(e) => setLocal({ ...local, name: e.target.value })}
              placeholder="源名称"
            />
            {!isLocal && (
              <Input
                value={local.base_url}
                onChange={(e) => setLocal({ ...local, base_url: e.target.value })}
                placeholder="https://peer.example.com"
              />
            )}
            {!isLocal && (
              <Input
                value={local.token || ''}
                onChange={(e) => setLocal({ ...local, token: e.target.value })}
                placeholder="访问令牌 (可选)"
              />
            )}
          </div>
          <div className="flex items-center gap-2">
            <label className="flex items-center gap-1.5 text-sm">
              <input type="checkbox" checked={local.enabled} onChange={(e) => setLocal({ ...local, enabled: e.target.checked })} />
              启用
            </label>
            {metaDirty && (
              <Button size="sm" onClick={() => onSave(local)}>
                <Save className="w-4 h-4" />
              </Button>
            )}
            {!isLocal && (
              <Button size="sm" variant="ghost" onClick={() => onDelete(source.id)}>
                <Trash2 className="w-4 h-4 text-rose-500" />
              </Button>
            )}
          </div>
        </div>

        {/* 已选模型摘要 */}
        <div className="flex items-center justify-between">
          <div className="text-sm text-muted-foreground">
            已选 <span className="font-semibold text-foreground">{source.models.length}</span> 个模型
            {source.type === 'remote_http' && !source.base_url && (
              <span className="ml-2 text-amber-500 inline-flex items-center gap-1">
                <AlertTriangle className="w-3.5 h-3.5" /> 请先填写并保存 base_url
              </span>
            )}
          </div>
          <Button variant="outline" size="sm" onClick={() => setExpanded((v) => !v)}>
            {expanded ? '收起' : '选择模型'}
            {expanded ? <ChevronUp className="w-4 h-4 ml-1" /> : <ChevronDown className="w-4 h-4 ml-1" />}
          </Button>
        </div>

        {/* 已选模型标签 */}
        {source.models.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {source.models.map((m) => (
              <span key={m} className="inline-flex items-center gap-1 px-2 py-0.5 text-xs rounded-md bg-secondary text-secondary-foreground">
                {m}
                <button onClick={() => toggleModel(m)} className="hover:text-rose-500">×</button>
              </span>
            ))}
          </div>
        )}

        {/* 模型选择器 */}
        {expanded && (
          <div className="border border-border rounded-lg p-3 space-y-3">
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <Search className="w-4 h-4 absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground" />
                <Input className="pl-8" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="搜索模型" />
              </div>
              <Button variant="outline" size="sm" onClick={loadModels} disabled={loadingModels}>
                <RefreshCw className={cn('w-4 h-4', loadingModels && 'animate-spin')} />
              </Button>
            </div>
            {loadingModels ? (
              <div className="flex items-center justify-center py-8 text-muted-foreground">
                <Loader2 className="w-5 h-5 animate-spin" />
              </div>
            ) : modelsError ? (
              <div className="text-sm text-rose-500 py-4 flex items-center gap-2">
                <AlertTriangle className="w-4 h-4" /> {modelsError}
              </div>
            ) : (
              <div className="max-h-64 overflow-y-auto grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-1">
                {filtered.map((m) => {
                  const selected = source.models.includes(m.model_name)
                  return (
                    <button
                      key={m.model_name}
                      onClick={() => toggleModel(m.model_name)}
                      className={cn(
                        'flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-md text-sm text-left transition-colors',
                        selected ? 'bg-primary/15 text-primary' : 'hover:bg-secondary',
                      )}
                    >
                      <span className="truncate">{m.model_name}</span>
                      {selected && <span className="text-xs">✓</span>}
                    </button>
                  )
                })}
                {filtered.length === 0 && (
                  <div className="col-span-full text-center py-6 text-sm text-muted-foreground">无匹配模型</div>
                )}
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// ============================================================================
// 小组件
// ============================================================================

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <label className="text-xs font-medium text-muted-foreground">{label}</label>
      {children}
    </div>
  )
}
