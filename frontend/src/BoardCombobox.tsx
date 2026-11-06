import { useEffect, useMemo, useRef, useState } from 'react'
import { boardOptionLabel, filterBoardOptions } from './boardSearch'
import type { BoardOption } from './boardSearch'

export function BoardCombobox({ value, options, onChange, placeholder = '输入名称、代码或拼音筛选' }: { value: string; options: BoardOption[]; onChange: (value: string) => void; placeholder?: string }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const container = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)

  const filtered = useMemo(() => filterBoardOptions(options, query), [options, query])
  const selected = options.find((option) => option.value === value)

  useEffect(() => {
    if (!open) return
    const closeOutside = (event: PointerEvent) => {
      if (!container.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', closeOutside)
    return () => document.removeEventListener('pointerdown', closeOutside)
  }, [open])

  useEffect(() => { setActiveIndex(0) }, [query, open])

  useEffect(() => {
    if (!open) return
    list.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [open, activeIndex, filtered])

  const close = () => {
    setOpen(false)
    setQuery('')
  }
  const choose = (option: BoardOption) => {
    onChange(option.value)
    close()
    input.current?.focus()
  }

  return <div className="board-combobox" ref={container}>
    <input ref={input} type="text" role="combobox" aria-expanded={open} aria-controls="board-combobox-list" aria-autocomplete="list"
      value={open ? query : selected ? boardOptionLabel(selected) : ''}
      placeholder={selected && !open ? undefined : placeholder}
      onFocus={() => { setOpen(true); setQuery('') }}
      onChange={(event) => { setQuery(event.target.value); setOpen(true) }}
      onBlur={(event) => { if (!container.current?.contains(event.relatedTarget as Node | null)) close() }}
      onKeyDown={(event) => {
        if (event.key === 'ArrowDown') {
          event.preventDefault()
          if (!open) { setOpen(true); return }
          setActiveIndex((current) => Math.min(current + 1, filtered.length - 1))
        } else if (event.key === 'ArrowUp') {
          event.preventDefault()
          if (!open) return
          setActiveIndex((current) => Math.max(current - 1, 0))
        } else if (event.key === 'Enter') {
          event.preventDefault()
          if (!open) return
          const option = filtered[activeIndex]
          if (option) choose(option)
        } else if (event.key === 'Escape') {
          if (open) { event.stopPropagation(); close() }
        }
      }}
    />
    {open && <ul id="board-combobox-list" className="board-combobox-menu" role="listbox" ref={list}>
      {filtered.map((option, index) => <li key={option.value} role="option" aria-selected={option.value === value} data-active={index === activeIndex ? 'true' : undefined} className="board-combobox-option" onPointerDown={(event) => event.preventDefault()} onClick={() => choose(option)}>
        <span>{option.name}</span><span className="board-combobox-code">{option.code}</span>
      </li>)}
      {filtered.length === 0 && <li className="board-combobox-empty" role="presentation">无匹配板块</li>}
    </ul>}
  </div>
}
