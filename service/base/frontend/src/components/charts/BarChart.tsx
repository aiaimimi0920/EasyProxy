interface BarData {
  label: string
  value: number
  color: string
}

interface BarChartProps {
  bars: BarData[]
  maxHeight?: number
  title?: string
}

export default function BarChart({ bars, maxHeight = 120, title }: BarChartProps) {
  const maxValue = Math.max(...bars.map(b => b.value), 1)

  if (bars.every(b => b.value === 0)) {
    return (
      <div className="flex flex-col items-center justify-center py-8">
        <div className="text-base-content/30 text-sm">暂无数据</div>
      </div>
    )
  }

  return (
    <div className="w-full">
      {title && (
        <h4 className="text-sm font-semibold mb-3 flex items-center gap-2">
          <span className="w-1 h-4 bg-primary rounded-full"></span>
          {title}
        </h4>
      )}
      <div className="flex items-end gap-2 justify-center" style={{ height: maxHeight + 32 }}>
        {bars.map((bar, i) => {
          const height = (bar.value / maxValue) * maxHeight
          return (
            <div key={i} className="flex flex-col items-center gap-1 flex-1 min-w-0">
              {/* Value label */}
              <span className="text-xs font-mono font-medium text-base-content/60 tabular-nums">
                {bar.value > 0 ? bar.value : ''}
              </span>
              {/* Bar */}
              <div
                className="w-full max-w-12 rounded-t-md transition-all duration-500 min-h-[2px]"
                style={{
                  height: Math.max(height, bar.value > 0 ? 4 : 2),
                  backgroundColor: bar.value > 0 ? bar.color : 'oklch(var(--bc) / 0.1)',
                }}
              />
              {/* Label */}
              <span className="text-[10px] text-base-content/50 text-center leading-tight whitespace-nowrap">
                {bar.label}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}