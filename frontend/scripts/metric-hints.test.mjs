import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const metricHintSource = readFileSync(new URL('../src/MetricHint.tsx', import.meta.url), 'utf8')
const linkageViewSource = readFileSync(new URL('../src/views/LinkageView.tsx', import.meta.url), 'utf8')
const quickPanelSource = readFileSync(new URL('../src/LinkageQuickPanel.tsx', import.meta.url), 'utf8')
const todayCoreSource = readFileSync(new URL('../src/TodayCorePanel.tsx', import.meta.url), 'utf8')
const linkageStyles = readFileSync(new URL('../src/views/linkage.css', import.meta.url), 'utf8')

const relationMetrics = [
  ['共同次数', 'c'],
  ['P(B|A) 条件共现率', 'c / a'],
  ['P(A|B) 反向共现率', 'c / b'],
  ['提升倍数 (Lift)', '(c / a) / (b / N) = cN / (ab)'],
  ['Jaccard', 'c / (a + b - c)'],
  ['热度折算共现', 'Σ 1 / max(1, 当天同池股票数 - 1)'],
  ['样本充分度', 'N ≥ 20 且 c ≥ 3'],
]

test('every linkage relation metric explains its formula and plain-language meaning', () => {
  for (const [label, formula] of relationMetrics) {
    const marker = `label="${label}" formula="${formula}"`
    const start = linkageViewSource.indexOf(marker)
    assert.ok(start >= 0, `missing hint for ${label}`)
    const block = linkageViewSource.slice(start, linkageViewSource.indexOf('</MetricHint>', start))
    assert.ok(block.split('\n').length >= 3, `${label} must include an explanation`)
  }
})

test('quick linkage cards explain the same conditional probability and lift', () => {
  for (const label of ['P(B|A) 条件共现率', '提升倍数 (Lift)']) {
    assert.ok(quickPanelSource.includes(`<MetricHint label="${label}"`), `missing quick hint for ${label}`)
  }
})

test('hint supports hover, keyboard focus, pinned tap and escape', () => {
  for (const event of ['onMouseEnter', 'onMouseLeave', 'onFocus', 'onBlur', 'onClick']) {
    assert.ok(metricHintSource.includes(event), `missing ${event}`)
  }
  assert.ok(metricHintSource.includes("event.key === 'Escape'"))
  assert.ok(metricHintSource.includes('aria-describedby={open ? tooltipId : undefined}'))
  assert.ok(metricHintSource.includes('role="tooltip"'))
})

test('today core explains its score and historical relationship metrics', () => {
  for (const label of [
    '核心度',
    '共同封板',
    'P(B|A) 条件共现率',
    'P(A|B) 反向共现率',
    '提升倍数 (Lift)',
    'Jaccard',
    'Phi 相关系数',
    '热度折算共现',
    '样本充分度',
  ]) {
    assert.ok(todayCoreSource.includes(`<MetricHint label="${label}"`), `missing today-core hint for ${label}`)
  }
  assert.ok(todayCoreSource.includes('今日盘中快照不写入历史，也不进入历史分母'))
  assert.ok(todayCoreSource.includes('共现不代表因果、带动或未来收益'))
})

test('sticky table headers outrank hints and constrained popovers stay usable', () => {
  const headerStart = linkageStyles.indexOf('.linkage-table th {')
  const headerBlock = linkageStyles.slice(headerStart, linkageStyles.indexOf('}', headerStart) + 1)
  assert.match(headerBlock, /z-index:\s*3/)
  const popoverStart = linkageStyles.indexOf('.metric-hint-popover {')
  const popoverBlock = linkageStyles.slice(popoverStart, linkageStyles.indexOf('\n}', popoverStart) + 2)
  assert.match(popoverBlock, /position:\s*fixed/)
  assert.match(popoverBlock, /z-index:\s*120/)
  assert.match(popoverBlock, /max-width:\s*calc\(100vw - 16px\)/)
  assert.match(popoverBlock, /max-height:\s*60vh/)
  assert.match(popoverBlock, /white-space:\s*normal/)
  assert.ok(metricHintSource.includes('createPortal('))
})
