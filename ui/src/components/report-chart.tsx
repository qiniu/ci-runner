import { useEffect, useRef } from "react"
import { useTheme } from "next-themes"
import { init, use as registerCharts, type ComposeOption, type EChartsType } from "echarts/core"
import {
  BarChart,
  LineChart,
  PieChart,
  type BarSeriesOption,
  type LineSeriesOption,
  type PieSeriesOption,
} from "echarts/charts"
import {
  GridComponent,
  TooltipComponent,
  type GridComponentOption,
  type TooltipComponentOption,
} from "echarts/components"
import { SVGRenderer } from "echarts/renderers"

registerCharts([BarChart, LineChart, PieChart, GridComponent, TooltipComponent, SVGRenderer])
export type ReportChartOption = ComposeOption<
  BarSeriesOption | LineSeriesOption | PieSeriesOption | GridComponentOption | TooltipComponentOption
>
export type ReportChartColors = { series: string[]; text: string; border: string; background: string }

// ECharts color operations require RGB; resolve the app's OKLCH tokens through
// the browser instead of maintaining a separate light/dark chart palette.
function themeColors(element: HTMLElement): ReportChartColors {
  const probe = document.createElement("span")
  element.appendChild(probe)
  const canvas = document.createElement("canvas")
  canvas.width = canvas.height = 1
  const context = canvas.getContext("2d")!
  const resolve = (token: string) => {
    probe.style.color = `var(--${token})`
    context.clearRect(0, 0, 1, 1)
    context.fillStyle = getComputedStyle(probe).color
    context.fillRect(0, 0, 1, 1)
    const [r, g, b, a] = context.getImageData(0, 0, 1, 1).data
    return `rgba(${r},${g},${b},${a / 255})`
  }
  const colors = {
    series: [1, 2, 3, 4, 5].map((n) => resolve(`chart-${n}`)),
    text: resolve("muted-foreground"),
    border: resolve("border"),
    background: resolve("card"),
  }
  probe.remove()
  return colors
}

export default function ReportChart({
  label,
  option,
}: {
  label: string
  option: (colors: ReportChartColors) => ReportChartOption
}) {
  const container = useRef<HTMLDivElement>(null)
  const instance = useRef<EChartsType | null>(null)
  const { resolvedTheme } = useTheme()
  useEffect(() => {
    const chart = init(container.current!, undefined, { renderer: "svg" })
    instance.current = chart
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(container.current!)
    return () => {
      observer.disconnect()
      instance.current = null
      chart.dispose()
    }
  }, [])
  useEffect(() => {
    const colors = themeColors(container.current!)
    instance.current?.setOption(
      {
        animation: false,
        color: colors.series,
        textStyle: { color: colors.text, fontFamily: getComputedStyle(container.current!).fontFamily },
        ...option(colors),
      },
      { notMerge: true },
    )
  }, [option, resolvedTheme])
  return <div ref={container} className="h-64 min-w-0" role="img" aria-label={label} />
}
