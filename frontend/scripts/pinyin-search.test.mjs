import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([new URL('../src/pinyinSearch.ts', import.meta.url)])
after(cleanup)
const { matchesSearchText } = await import(urls.pinyinSearch)

test('pinyin initials, full pinyin and latin text all match case-insensitively', () => {
  assert.equal(matchesSearchText('人形机器人', 'rxjqr'), true)
  assert.equal(matchesSearchText('人形机器人', 'RXJQR'), true)
  assert.equal(matchesSearchText('人形机器人', 'renxingjiqiren'), true)
  assert.equal(matchesSearchText('平安银行', 'payh'), true)
  assert.equal(matchesSearchText('平安银行', 'yinhang'), true)
  assert.equal(matchesSearchText('万科A', 'wka'), true)
  assert.equal(matchesSearchText('万科A', 'wankea'), true)
  assert.equal(matchesSearchText('AI算力', 'aisl'), true)
})

test('chinese-only queries stay plain substring matches', () => {
  assert.equal(matchesSearchText('人形机器人', '机器人'), true)
  assert.equal(matchesSearchText('人形机器人', '白酒'), false)
  assert.equal(matchesSearchText('人形机器人', ''), true)
  assert.equal(matchesSearchText('人形机器人', '   '), true)
})

test('the recorded readings are the real ones, not approximations', () => {
  assert.equal(matchesSearchText('人形机器人', 'rjqr'), false)
  assert.equal(matchesSearchText('人形机器人', '000001'), false)
})
