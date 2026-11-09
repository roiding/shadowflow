export function shiftISODate(value: string, days: number) {
  const [year, month, day] = value.split('-').map(Number)
  const date = new Date(Date.UTC(year, month - 1, day + days))
  return date.toISOString().slice(0, 10)
}

export function latestArchivedDay(days: readonly string[]) {
  return days.at(-1) ?? ''
}

export function trendWindowEndingAt(end: string, lookbackDays = 30) {
  return { from: shiftISODate(end, -lookbackDays), to: end }
}

export function firstArchivedDayInRange(days: readonly string[], from: string, to: string) {
  return days.find((day) => day >= from && day <= to) ?? ''
}
