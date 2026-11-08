import type { LimitUpEvent, LimitUpLink, LimitUpStock } from './api/limitup'
import { matchesSearchText } from './pinyinSearch'

export type LinkageEventType = 'seal' | 'touch' | 'down'

export interface GraphNodeLayout {
  code: string
  name: string
  coordX: number
  coordY: number
  radius: number
  isCenter: boolean
  overlapDays?: number
  conditionalProbability?: number
  reverseProbability?: number
}

export interface GraphEdgeLayout {
  targetCode: string
  targetName: string
  startX: number
  startY: number
  endX: number
  endY: number
  overlapDays: number
  strokeWidth: number
  strokeOpacity: number
  conditionalProbability: number
  reverseProbability: number
}

export interface GraphLayoutResult {
  centerNode: GraphNodeLayout | null
  neighborNodes: GraphNodeLayout[]
  edges: GraphEdgeLayout[]
}

export function getShanghaiDateString(dateInstance?: Date): string {
  const referenceDate = dateInstance ?? new Date()
  return new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(referenceDate)
}

export function formatPercent(ratioValue: number | null | undefined, fractionDigits = 1): string {
  if (ratioValue === null || ratioValue === undefined || !Number.isFinite(ratioValue)) {
    return '--'
  }
  return `${(ratioValue * 100).toFixed(fractionDigits)}%`
}

export function formatPercentPoint(percentValue: number | null | undefined, fractionDigits = 2): string {
  if (percentValue === null || percentValue === undefined || !Number.isFinite(percentValue)) {
    return '--'
  }
  return `${percentValue.toFixed(fractionDigits)}%`
}

export function formatNumber(numericValue: number | null | undefined, fractionDigits = 0): string {
  if (numericValue === null || numericValue === undefined || !Number.isFinite(numericValue)) {
    return '--'
  }
  return new Intl.NumberFormat('zh-CN', {
    maximumFractionDigits: fractionDigits,
    minimumFractionDigits: fractionDigits,
  }).format(numericValue)
}

export function formatOpenCountDisplay(openCount: number | null | undefined): string {
  if (openCount === null || openCount === undefined) {
    return '未知'
  }
  return `${openCount}次`
}

export function formatPoolStatus(poolType: string): { label: string; className: string } {
  if (poolType === 'limit_up') {
    return { label: '涨停', className: 'seal' }
  }
  if (poolType === 'open_limit') {
    return { label: '炸板', className: 'broken' }
  }
  if (poolType === 'limit_down') {
    return { label: '跌停', className: 'down' }
  }
  return { label: poolType, className: 'default' }
}

export function formatEventRowKey(eventRecord: Pick<LimitUpEvent, 'trade_date' | 'pool'>): string {
  return `${eventRecord.trade_date}:${eventRecord.pool}`
}

export interface EventTouchTimes {
  first: string | null
  last: string | null
  firstLabel: string
  lastLabel: string
  side: 'up' | 'down'
}

export function eventTouchTimes(
  eventRecord: Pick<
    LimitUpEvent,
    'pool' | 'first_limit_at' | 'last_limit_at' | 'first_down_at' | 'last_down_at'
  >,
): EventTouchTimes {
  if (eventRecord.pool === 'limit_down') {
    return {
      first: eventRecord.first_down_at ?? null,
      last: eventRecord.last_down_at ?? null,
      firstLabel: '首次触及跌停',
      lastLabel: '末次触及跌停',
      side: 'down',
    }
  }
  return {
    first: eventRecord.first_limit_at,
    last: eventRecord.last_limit_at,
    firstLabel: '首次触及涨停',
    lastLabel: '末次触及涨停',
    side: 'up',
  }
}

export function formatTimeText(rawTime: string | null | undefined): string {
  if (!rawTime) {
    return '--'
  }
  const trimmed = rawTime.trim()
  if (/^\d{1,2}:\d{2}(:\d{2})?$/.test(trimmed)) {
    return trimmed
  }
  if (/^\d{9,13}$/.test(trimmed)) {
    const numericEpoch = Number(trimmed)
    const epochMilliseconds = numericEpoch < 1e11 ? numericEpoch * 1000 : numericEpoch
    const timestampDate = new Date(epochMilliseconds)
    if (!Number.isNaN(timestampDate.getTime())) {
      return timestampDate.toLocaleTimeString('zh-CN', {
        timeZone: 'Asia/Shanghai',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      })
    }
  }
  const parsedDate = new Date(trimmed)
  if (!Number.isNaN(parsedDate.getTime())) {
    return parsedDate.toLocaleTimeString('zh-CN', {
      timeZone: 'Asia/Shanghai',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    })
  }
  return trimmed
}

export function sortStockCandidates(
  stockList: LimitUpStock[],
  eventType: LinkageEventType = 'seal',
): LimitUpStock[] {
  return [...stockList].sort((stockCandidateFirst, stockCandidateSecond) => {
    if (eventType === 'down') {
      const downDayDifference = stockCandidateSecond.down_days - stockCandidateFirst.down_days
      if (downDayDifference !== 0) {
        return downDayDifference
      }
    }

    const primaryFirst = eventType === 'touch' ? stockCandidateFirst.touch_days : stockCandidateFirst.seal_days
    const primarySecond = eventType === 'touch' ? stockCandidateSecond.touch_days : stockCandidateSecond.seal_days
    if (primarySecond !== primaryFirst) {
      return primarySecond - primaryFirst
    }

    const secondaryFirst = eventType === 'touch' ? stockCandidateFirst.seal_days : stockCandidateFirst.touch_days
    const secondarySecond = eventType === 'touch' ? stockCandidateSecond.seal_days : stockCandidateSecond.touch_days
    if (secondarySecond !== secondaryFirst) {
      return secondarySecond - secondaryFirst
    }

    if (stockCandidateSecond.down_days !== stockCandidateFirst.down_days) {
      return stockCandidateSecond.down_days - stockCandidateFirst.down_days
    }

    return stockCandidateFirst.code.localeCompare(stockCandidateSecond.code)
  })
}

export function filterStockCandidates(stockList: LimitUpStock[], searchKeyword: string): LimitUpStock[] {
  const trimmedQuery = searchKeyword.trim().toLowerCase()
  if (!trimmedQuery) {
    return stockList
  }
  return stockList.filter((candidate) => {
    if (candidate.code.toLowerCase().includes(trimmedQuery)) {
      return true
    }
    return matchesSearchText(candidate.name, trimmedQuery)
  })
}

export function calculateGraphCoordinates(
  centerStock: { code: string; name: string } | null,
  linkList: LimitUpLink[],
  svgWidth: number,
  svgHeight: number,
  maxNeighbors = 14,
): GraphLayoutResult {
  if (!centerStock) {
    return { centerNode: null, neighborNodes: [], edges: [] }
  }

  const centerX = Math.round(svgWidth / 2)
  const centerY = Math.round(svgHeight / 2)
  const centerNode: GraphNodeLayout = {
    code: centerStock.code,
    name: centerStock.name,
    coordX: centerX,
    coordY: centerY,
    radius: 30,
    isCenter: true,
  }

  const visibleLinks = linkList.slice(0, maxNeighbors)
  if (visibleLinks.length === 0) {
    return { centerNode, neighborNodes: [], edges: [] }
  }

  let minOverlap = visibleLinks[0].overlap_days
  let maxOverlap = visibleLinks[0].overlap_days
  for (const linkItem of visibleLinks) {
    if (linkItem.overlap_days < minOverlap) {
      minOverlap = linkItem.overlap_days
    }
    if (linkItem.overlap_days > maxOverlap) {
      maxOverlap = linkItem.overlap_days
    }
  }

  const orbitRadius = Math.min(centerX, centerY) * 0.72
  const neighborNodes: GraphNodeLayout[] = []
  const edges: GraphEdgeLayout[] = []
  const totalCount = visibleLinks.length

  visibleLinks.forEach((linkItem, itemIndex) => {
    const angleRadian = (itemIndex / totalCount) * 2 * Math.PI - Math.PI / 2
    const nodeX = Math.round(centerX + orbitRadius * Math.cos(angleRadian))
    const nodeY = Math.round(centerY + orbitRadius * Math.sin(angleRadian))

    const strokeRatio = maxOverlap === minOverlap
      ? 0.5
      : (linkItem.overlap_days - minOverlap) / Math.max(1, maxOverlap - minOverlap)

    const computedStroke = 1.5 + strokeRatio * 4.5
    const computedOpacity = 0.35 + strokeRatio * 0.55

    neighborNodes.push({
      code: linkItem.code,
      name: linkItem.name,
      coordX: nodeX,
      coordY: nodeY,
      radius: 22,
      isCenter: false,
      overlapDays: linkItem.overlap_days,
      conditionalProbability: linkItem.conditional_probability,
      reverseProbability: linkItem.reverse_probability,
    })

    edges.push({
      targetCode: linkItem.code,
      targetName: linkItem.name,
      startX: centerX,
      startY: centerY,
      endX: nodeX,
      endY: nodeY,
      overlapDays: linkItem.overlap_days,
      strokeWidth: Number(computedStroke.toFixed(1)),
      strokeOpacity: Number(computedOpacity.toFixed(2)),
      conditionalProbability: linkItem.conditional_probability,
      reverseProbability: linkItem.reverse_probability,
    })
  })

  return { centerNode, neighborNodes, edges }
}

export function paginateRecords<RecordType>(
  recordList: RecordType[],
  targetPage: number,
  rowsPerPage: number,
): {
  paginatedItems: RecordType[]
  totalPages: number
  currentPage: number
} {
  const safePageSize = Math.max(1, rowsPerPage)
  const totalPages = Math.max(1, Math.ceil(recordList.length / safePageSize))
  const currentPage = Math.min(Math.max(1, targetPage), totalPages)
  const startIndex = (currentPage - 1) * safePageSize
  const paginatedItems = recordList.slice(startIndex, startIndex + safePageSize)
  return { paginatedItems, totalPages, currentPage }
}
