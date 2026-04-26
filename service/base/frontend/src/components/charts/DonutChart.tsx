interface DonutSegment {
  label: string
  value: number
  color: string
}

interface DonutChartProps {
  segments: DonutSegment[]
  size?: number
  strokeWidth?: number
  centerLabel?: string
  centerValue?: string
}

export default function DonutChart({
  segments,
  size = 160,
  strokeWidth = 28,
  centerLabel,
  centerValue,
}: DonutChartProps) {
  const radius = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * radius
  const center = size / 2
  const total = segments.reduce((sum, s) => sum + s.value, 0)

  if (total === 0) {
    return (
      <div className="flex flex-col items-center justify-center" style={{ width: size, height: size }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
          <circle
            cx={center}
            cy={center}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={strokeWidth}
            className="text-base-300/50"
          />
        </svg>
        <div className="absolute text-center">
          <div className="text-2xl font-bold text-base-content/30">-</div>
          <div className="text-xs text-base-content/30">无数据</div>
        </div>
      </div>
    )
  }

  const filteredSegments = segments.filter(s => s.value > 0)
  const arcs = filteredSegments.reduce<Array<DonutSegment & {
    dashLength: number
    gapLength: number
    dashOffset: number
    percentage: number
  }>>((acc, segment) => {
    const accumulated = acc.reduce((sum, a) => sum + a.value / total, 0)
    const ratio = segment.value / total
    const dashLength = ratio * circumference
    const dashOffset = -accumulated * circumference + circumference * 0.25
    acc.push({
      ...segment,
      dashLength,
      gapLength: circumference - dashLength,
      dashOffset,
      percentage: Math.round(ratio * 100),
    })
    return acc
  }, [])

  return (
    <div className="flex flex-col items-center gap-3">
      <div className="relative" style={{ width: size, height: size }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
          {/* Background circle */}
          <circle
            cx={center}
            cy={center}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={strokeWidth}
            className="text-base-300/30"
          />
          {/* Segments */}
          {arcs.map((arc, i) => (
            <circle
              key={i}
              cx={center}
              cy={center}
              r={radius}
              fill="none"
              stroke={arc.color}
              strokeWidth={strokeWidth}
              strokeDasharray={`${arc.dashLength} ${arc.gapLength}`}
              strokeDashoffset={arc.dashOffset}
              strokeLinecap="butt"
              className="transition-all duration-500"
            />
          ))}
        </svg>
        {/* Center text */}
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          {centerValue && (
            <div className="text-2xl font-bold">{centerValue}</div>
          )}
          {centerLabel && (
            <div className="text-xs text-base-content/50">{centerLabel}</div>
          )}
        </div>
      </div>
      {/* Legend */}
      <div className="flex flex-wrap justify-center gap-x-4 gap-y-1">
        {arcs.map((arc, i) => (
          <div key={i} className="flex items-center gap-1.5 text-xs">
            <span
              className="w-2.5 h-2.5 rounded-full shrink-0"
              style={{ backgroundColor: arc.color }}
            />
            <span className="text-base-content/70">{arc.label}</span>
            <span className="font-mono font-medium">{arc.value}</span>
            <span className="text-base-content/40">({arc.percentage}%)</span>
          </div>
        ))}
      </div>
    </div>
  )
}