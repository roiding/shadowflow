import { pinyin } from 'pinyin-pro'

export type PinyinIndex = { full: string; initials: string }

const pinyinCache = new Map<string, PinyinIndex>()

export function pinyinIndexOf(text: string): PinyinIndex {
  const cached = pinyinCache.get(text)
  if (cached) return cached
  const full = pinyin(text, { toneType: 'none', type: 'array', nonZh: 'consecutive' }).join('')
  const initials = pinyin(text, { pattern: 'first', toneType: 'none', type: 'array', nonZh: 'consecutive' }).join('')
  const entry = { full: full.toLowerCase(), initials: initials.toLowerCase() }
  pinyinCache.set(text, entry)
  return entry
}

export function matchesSearchText(text: string, query: string): boolean {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return true
  if (text.toLowerCase().includes(normalized)) return true
  if (!/[a-z]/.test(normalized)) return false
  const index = pinyinIndexOf(text)
  return index.full.includes(normalized) || index.initials.includes(normalized)
}
