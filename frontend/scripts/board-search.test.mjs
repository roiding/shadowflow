import assert from 'node:assert/strict'
import test, { after } from 'node:test'

import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([new URL('../src/boardSearch.ts', import.meta.url), new URL('../src/pinyinSearch.ts', import.meta.url)])
after(cleanup)
const { filterBoardOptions, boardOptionLabel } = await import(urls.boardSearch)

const options = [
  { value: 'BK0490', name: '半导体', code: 'BK0490' },
  { value: 'BK1037', name: '人形机器人', code: 'BK1037' },
  { value: 'BK0800', name: 'AI算力', code: 'BK0800' },
  { value: 'BK0475', name: '银行', code: 'BK0475' },
]

test('empty or blank queries keep the archived rank order', () => {
  assert.deepEqual(filterBoardOptions(options, ''), options)
  assert.deepEqual(filterBoardOptions(options, '   '), options)
})

test('names and codes match case-insensitive fragments', () => {
  assert.deepEqual(filterBoardOptions(options, '机器人'), [options[1]])
  assert.deepEqual(filterBoardOptions(options, '算力'), [options[2]])
  assert.deepEqual(filterBoardOptions(options, 'ai'), [options[2]])
  assert.deepEqual(filterBoardOptions(options, '490'), [options[0]])
  assert.deepEqual(filterBoardOptions(options, 'bk0475'), [options[3]])
  assert.deepEqual(filterBoardOptions(options, '白酒'), [])
})

test('pinyin initials and full pinyin match after direct text misses', () => {
  assert.deepEqual(filterBoardOptions(options, 'rxjqr').map((option) => option.value), ['BK1037'])
  assert.deepEqual(filterBoardOptions(options, 'renxingjiqiren').map((option) => option.value), ['BK1037'])
  assert.deepEqual(filterBoardOptions(options, 'bank'), [])
})

test('name prefix outranks name substring, which outranks code match', () => {
  const ranked = [
    { value: 'a', name: '机器人概念', code: 'BK1000' },
    { value: 'b', name: '人形机器人', code: 'BK1001' },
    { value: 'c', name: '其他', code: 'BK2000' },
  ]
  assert.deepEqual(filterBoardOptions(ranked, '机器人').map((option) => option.value), ['a', 'b'])
  assert.deepEqual(filterBoardOptions(ranked, '2000').map((option) => option.value), ['c'])
})

test('option label keeps the legacy display format', () => {
  assert.equal(boardOptionLabel(options[0]), '半导体 (BK0490)')
})
