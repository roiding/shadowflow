import { matchesSearchText } from './pinyinSearch'

export type BoardOption = { value: string; name: string; code: string }

export function filterBoardOptions(options: BoardOption[], query: string): BoardOption[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return options
  const matches: Array<{ option: BoardOption; score: number }> = []
  for (const option of options) {
    const name = option.name.toLowerCase()
    const code = option.code.toLowerCase()
    if (name.startsWith(needle)) matches.push({ option, score: 0 })
    else if (name.includes(needle)) matches.push({ option, score: 1 })
    else if (code.includes(needle)) matches.push({ option, score: 2 })
    else if (matchesSearchText(option.name, needle)) matches.push({ option, score: 3 })
  }
  return matches.sort((a, b) => a.score - b.score).map((item) => item.option)
}

export function boardOptionLabel(option: BoardOption): string {
  return `${option.name} (${option.code})`
}
