import ReactEChartsCore from 'echarts-for-react/lib/core'
import type { EChartsReactProps } from 'echarts-for-react/lib/types'
import * as echarts from 'echarts/core'
import { BarChart, LineChart, ScatterChart } from 'echarts/charts'
import {
  AxisPointerComponent,
  GridComponent,
  LegendComponent,
  TitleComponent,
  TooltipComponent,
} from 'echarts/components'
import { LabelLayout } from 'echarts/features'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  BarChart,
  LineChart,
  ScatterChart,
  AxisPointerComponent,
  GridComponent,
  LegendComponent,
  TitleComponent,
  TooltipComponent,
  LabelLayout,
  CanvasRenderer,
])

export function DashboardECharts(props: EChartsReactProps) {
  return <ReactEChartsCore echarts={echarts} {...props} />
}
