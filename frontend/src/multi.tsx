import React from 'react'
import ReactDOM from 'react-dom/client'
import { MultiSourceEmbed } from './components/MultiSourceEmbed'
import './index.css'

// URL 参数可覆盖主题与刷新间隔 (便于 iframe 嵌入时强制指定)
const urlParams = new URLSearchParams(window.location.search)
const themeOverride = urlParams.get('theme') || undefined
const refreshOverride = urlParams.get('refresh')

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <MultiSourceEmbed
      themeOverride={themeOverride}
      refreshOverride={refreshOverride ? parseInt(refreshOverride, 10) : undefined}
    />
  </React.StrictMode>,
)
