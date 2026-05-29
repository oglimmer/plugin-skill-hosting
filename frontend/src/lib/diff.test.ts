import { describe, it, expect } from 'vitest'
import {
  diffLines,
  diffWords,
  buildLineDiff,
  buildSplitRows,
  collapse,
  documentsDiffer,
  type RichLine,
} from './diff'

// Compact view of a diff line for readable assertions.
const sig = (l: RichLine) => `${l.type[0]}${l.oldNumber ?? '_'}:${l.newNumber ?? '_'} ${l.text}`

describe('diffLines', () => {
  it('marks identical documents as all-equal', () => {
    const r = diffLines('a\nb\nc', 'a\nb\nc')
    expect(r.map(l => l.type)).toEqual(['equal', 'equal', 'equal'])
    expect(r.map(sig)).toEqual(['e1:1 a', 'e2:2 b', 'e3:3 c'])
  })

  it('detects an inserted line with correct numbering', () => {
    const r = diffLines('a\nc', 'a\nb\nc')
    expect(r.map(sig)).toEqual(['e1:1 a', 'i_:2 b', 'e2:3 c'])
  })

  it('detects a deleted line with correct numbering', () => {
    const r = diffLines('a\nb\nc', 'a\nc')
    expect(r.map(sig)).toEqual(['e1:1 a', 'd2:_ b', 'e3:2 c'])
  })

  it('represents a replaced line as delete then insert', () => {
    const r = diffLines('a\nB\nc', 'a\nX\nc')
    expect(r.map(l => l.type)).toEqual(['equal', 'delete', 'insert', 'equal'])
  })

  it('treats an empty old document as all inserts', () => {
    const r = diffLines('', 'a\nb')
    expect(r.map(l => l.type)).toEqual(['insert', 'insert'])
    expect(r.map(l => l.oldNumber)).toEqual([null, null])
    expect(r.map(l => l.newNumber)).toEqual([1, 2])
  })

  it('treats an empty new document as all deletes', () => {
    const r = diffLines('a\nb', '')
    expect(r.map(l => l.type)).toEqual(['delete', 'delete'])
    expect(r.map(l => l.newNumber)).toEqual([null, null])
  })
})

describe('diffLines with ignoreWhitespace', () => {
  it('treats indentation-only changes as equal', () => {
    const r = diffLines('  foo\nbar', 'foo\n    bar', { ignoreWhitespace: true })
    expect(r.map(l => l.type)).toEqual(['equal', 'equal'])
    // matched lines render the new text
    expect(r.map(l => l.text)).toEqual(['foo', '    bar'])
  })

  it('treats collapsed internal spaces as equal', () => {
    const r = diffLines('a   b', 'a b', { ignoreWhitespace: true })
    expect(r.map(l => l.type)).toEqual(['equal'])
  })

  it('still flags a real content change when ignoring whitespace', () => {
    const r = diffLines('  foo', '  bar', { ignoreWhitespace: true })
    expect(r.map(l => l.type)).toEqual(['delete', 'insert'])
  })

  it('without the flag, whitespace differences are real changes', () => {
    const r = diffLines('  foo', 'foo')
    expect(r.map(l => l.type)).toEqual(['delete', 'insert'])
  })
})

describe('documentsDiffer', () => {
  it('is false for identical text', () => {
    expect(documentsDiffer('a\nb', 'a\nb')).toBe(false)
  })
  it('is true for any difference by default', () => {
    expect(documentsDiffer('a\nb', 'a\n  b')).toBe(true)
  })
  it('ignores whitespace-only differences when asked', () => {
    expect(documentsDiffer('a\n  b', 'a\nb', { ignoreWhitespace: true })).toBe(false)
    expect(documentsDiffer('a\n  b', 'a\nc', { ignoreWhitespace: true })).toBe(true)
  })
  it('ignores added/removed blank lines when asked', () => {
    expect(documentsDiffer('a\nb', 'a\n\n\nb', { ignoreWhitespace: true })).toBe(false)
  })
})

describe('diffWords', () => {
  it('highlights only the tokens that changed', () => {
    const { old, new: nw, common } = diffWords('the quick fox', 'the slow fox')
    expect(old.filter(s => s.changed).map(s => s.text)).toEqual(['quick'])
    expect(nw.filter(s => s.changed).map(s => s.text)).toEqual(['slow'])
    // unchanged: "the ", " fox"
    expect(common).toBeGreaterThan(0)
    expect(old.map(s => s.text).join('')).toBe('the quick fox')
    expect(nw.map(s => s.text).join('')).toBe('the slow fox')
  })

  it('merges adjacent same-kind tokens into one segment', () => {
    const { new: nw } = diffWords('a', 'a b c')
    // the trailing " b c" is all added and should collapse to a single segment
    expect(nw.filter(s => s.changed).length).toBe(1)
  })
})

describe('buildLineDiff', () => {
  it('attaches word segments to similar replaced lines', () => {
    const lines = buildLineDiff('hello world', 'hello there')
    const del = lines.find(l => l.type === 'delete')!
    const ins = lines.find(l => l.type === 'insert')!
    expect(del.segments).not.toBeNull()
    expect(ins.segments).not.toBeNull()
    expect(del.segments!.some(s => !s.changed && s.text.includes('hello'))).toBe(true)
  })

  it('mutes inserted blank lines when ignoring whitespace', () => {
    // v4 added two blank lines between "a" and "b"
    const lines = buildLineDiff('a\nb', 'a\n\n\nb', { ignoreWhitespace: true })
    const inserted = lines.filter(l => l.type === 'insert')
    expect(inserted).toHaveLength(2)
    expect(inserted.every(l => l.muted)).toBe(true)
    // ...and isChangeLine ignores them, so the doc reads as unchanged
    expect(lines.some(l => l.type === 'delete')).toBe(false)
  })

  it('does not mute blank-line inserts without the flag', () => {
    const lines = buildLineDiff('a\nb', 'a\n\nb')
    expect(lines.find(l => l.type === 'insert')?.muted).toBeUndefined()
  })

  it('leaves segments null for wholesale rewrites', () => {
    const lines = buildLineDiff('aaaaaaaaaa', 'zzzzzzzzzz')
    const del = lines.find(l => l.type === 'delete')!
    const ins = lines.find(l => l.type === 'insert')!
    expect(del.segments).toBeNull()
    expect(ins.segments).toBeNull()
  })
})

describe('buildSplitRows', () => {
  it('puts equal lines on both sides', () => {
    const rows = buildSplitRows(buildLineDiff('a', 'a'))
    expect(rows).toHaveLength(1)
    expect(rows[0].left).toBe(rows[0].right)
  })

  it('pairs a replaced line left/right on one row', () => {
    const rows = buildSplitRows(buildLineDiff('a\nB\nc', 'a\nX\nc'))
    const changed = rows.find(r => r.left?.type === 'delete')!
    expect(changed.left!.text).toBe('B')
    expect(changed.right!.text).toBe('X')
  })

  it('leaves the opposite side null for unpaired add/delete', () => {
    const rows = buildSplitRows(buildLineDiff('a', 'a\nb'))
    const added = rows.find(r => r.right?.type === 'insert')!
    expect(added.left).toBeNull()
    expect(added.right!.text).toBe('b')
  })
})

describe('collapse', () => {
  const lines = (texts: string[], changeIdx: number[]): RichLine[] =>
    texts.map((text, i) => ({
      type: changeIdx.includes(i) ? 'insert' : 'equal',
      text,
      oldNumber: null,
      newNumber: i + 1,
      segments: null,
    }))

  const isChange = (l: RichLine) => l.type !== 'equal'

  it('keeps everything when there are no changes (single lines group, no gap)', () => {
    const groups = collapse(lines(['a', 'b', 'c'], []), isChange)
    // No change anchors → nothing kept → the whole run is one gap only if long
    // enough; with 3 lines and minGap 4 it stays a single lines group.
    expect(groups).toHaveLength(1)
    expect(groups[0].type).toBe('lines')
  })

  it('collapses a long unchanged run between two changes into a gap', () => {
    // change at 0 and 12, with a long equal stretch between them
    const texts = Array.from({ length: 13 }, (_, i) => `l${i}`)
    const groups = collapse(lines(texts, [0, 12]), isChange, 3, 4)
    expect(groups.map(g => g.type)).toEqual(['lines', 'gap', 'lines'])
    // context=3 around index 0 keeps 0..3; around 12 keeps 9..12; gap = 4..8 (5 lines)
    expect(groups[1].items.map(i => i.text)).toEqual(['l4', 'l5', 'l6', 'l7', 'l8'])
  })

  it('does not collapse a short gap below minGap', () => {
    const texts = Array.from({ length: 8 }, (_, i) => `l${i}`)
    // changes at 0 and 7; context 3 keeps 0..3 and 4..7 → no real gap
    const groups = collapse(lines(texts, [0, 7]), isChange, 3, 4)
    expect(groups.every(g => g.type === 'lines')).toBe(true)
  })
})
