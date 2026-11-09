import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { HelpCircle } from 'lucide-react'

export function MetricHint({
  label,
  formula,
  children,
  align = 'center',
}: {
  label: string
  formula?: string
  children: string
  align?: 'start' | 'center'
}) {
  const tooltipId = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popoverRef = useRef<HTMLSpanElement>(null)
  const closeTimerRef = useRef<number>(undefined)
  const [hovered, setHovered] = useState(false)
  const [focused, setFocused] = useState(false)
  const [pinned, setPinned] = useState(false)
  const [position, setPosition] = useState<{ left: number; top: number }>()
  const open = hovered || focused || pinned

  const keepHoverOpen = () => {
    window.clearTimeout(closeTimerRef.current)
    setHovered(true)
  }
  const closeHoverSoon = () => {
    window.clearTimeout(closeTimerRef.current)
    closeTimerRef.current = window.setTimeout(() => setHovered(false), 120)
  }

  useLayoutEffect(() => {
    if (!open) {
      setPosition(undefined)
      return
    }
    const update = () => {
      const trigger = triggerRef.current
      const popover = popoverRef.current
      if (!trigger || !popover) return
      const margin = 8
      const gap = 7
      const triggerRect = trigger.getBoundingClientRect()
      const popoverRect = popover.getBoundingClientRect()
      const preferredLeft = align === 'center'
        ? triggerRect.left + triggerRect.width / 2 - popoverRect.width / 2
        : triggerRect.left
      const left = Math.min(
        Math.max(preferredLeft, margin),
        window.innerWidth - popoverRect.width - margin,
      )
      let top = triggerRect.bottom + gap
      if (top + popoverRect.height > window.innerHeight - margin) {
        top = Math.max(margin, triggerRect.top - popoverRect.height - gap)
      }
      setPosition({ left, top })
    }
    update()
    let frame = 0
    const listen = () => {
      window.cancelAnimationFrame(frame)
      frame = window.requestAnimationFrame(update)
    }
    window.addEventListener('resize', listen)
    window.addEventListener('scroll', listen, true)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', listen)
      window.removeEventListener('scroll', listen, true)
    }
  }, [align, open])

  useEffect(() => {
    if (!open) return
    const closeOnOutsidePress = (event: PointerEvent) => {
      if (!pinned) return
      const target = event.target as Node
      if (!triggerRef.current?.contains(target) && !popoverRef.current?.contains(target)) {
        setPinned(false)
      }
    }
    document.addEventListener('pointerdown', closeOnOutsidePress)
    return () => document.removeEventListener('pointerdown', closeOnOutsidePress)
  }, [open, pinned])

  useEffect(() => () => window.clearTimeout(closeTimerRef.current), [])

  return (
    <span className="metric-hint">
      <button
        ref={triggerRef}
        type="button"
        className="metric-hint-button"
        aria-label={`${label}说明`}
        aria-describedby={open ? tooltipId : undefined}
        aria-expanded={open}
        onClick={(event) => {
          event.stopPropagation()
          if (pinned) {
            setPinned(false)
            setHovered(false)
            setFocused(false)
            event.currentTarget.blur()
          } else {
            setPinned(true)
          }
        }}
        onMouseEnter={keepHoverOpen}
        onMouseLeave={closeHoverSoon}
        onFocus={() => setFocused(true)}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            setPinned(false)
            setHovered(false)
            setFocused(false)
            event.currentTarget.blur()
          }
        }}
        onBlur={() => {
          setFocused(false)
          setPinned(false)
        }}
      >
        <HelpCircle size={13} aria-hidden="true" />
      </button>
      {open && createPortal(
        <span
          ref={popoverRef}
          id={tooltipId}
          role="tooltip"
          className="metric-hint-popover"
          style={{ ...position, visibility: position ? 'visible' : 'hidden' }}
          onMouseEnter={keepHoverOpen}
          onMouseLeave={closeHoverSoon}
        >
          <strong>{label}</strong>
          {formula && <code>{formula}</code>}
          <span>{children}</span>
        </span>,
        document.body,
      )}
    </span>
  )
}
