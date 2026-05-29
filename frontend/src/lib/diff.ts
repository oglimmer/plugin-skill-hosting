// Small, dependency-free text-diff core used by the skill version compare view.
//
// We deliberately avoid pulling in a diff library: the project keeps its
// dependency surface minimal, and a classic LCS diff over lines (plus a second
// pass over tokens for intra-line highlighting) is compact and easy to test.
//
// Complexity is O(n·m) in lines/tokens, which is fine for skill documents
// (hundreds of lines at most).

export type LineType = 'equal' | 'insert' | 'delete'

/** A run of text within a changed line, flagged if it differs from its pair. */
export interface Segment {
  text: string
  changed: boolean
}

export interface RichLine {
  type: LineType
  text: string
  /** 1-based line number in the old text, or null for inserts. */
  oldNumber: number | null
  /** 1-based line number in the new text, or null for deletes. */
  newNumber: number | null
  /**
   * Word-level segments, set only when this line is paired with its
   * counterpart in a replace block and the two are similar enough to be worth
   * highlighting. Null means "render the whole line as plain add/delete".
   */
  segments: Segment[] | null
  /**
   * A change that should be displayed as neutral context rather than an
   * add/delete. Set on whitespace-only inserted/deleted lines when
   * ignoreWhitespace is on, so blank-line churn neither tints nor counts as a
   * change (and collapses into the unchanged-context bands).
   */
  muted?: boolean
}

/** A line that visually reads as a real add/delete (not equal, not muted). */
export function isChangeLine(l: RichLine | null | undefined): boolean {
  return !!l && l.type !== 'equal' && !l.muted
}

export interface SplitRow {
  left: RichLine | null
  right: RichLine | null
}

export interface Group<T> {
  type: 'lines' | 'gap'
  items: T[]
}

export interface DiffOptions {
  /**
   * Treat lines/tokens that differ only in whitespace as equal. Collapses
   * internal whitespace runs and trims ends before comparing, so re-indentation
   * and trailing-space churn don't show up as changes.
   */
  ignoreWhitespace?: boolean
}

// Canonical form for whitespace-insensitive comparison: collapse every run of
// whitespace to a single space and trim the ends.
function normalizeLine(s: string): string {
  return s.replace(/\s+/g, ' ').trim()
}

// An empty document is zero lines (so the first add/last delete reads cleanly),
// not one empty line as a naive `''.split('\n')` would give.
function splitLines(s: string): string[] {
  return s === '' ? [] : s.split('\n')
}

// Tokenise into runs of whitespace, word characters, and individual symbols.
// Keeping whitespace as its own token means reflowed prose highlights only the
// words that actually changed, not the spaces around them.
function tokenize(s: string): string[] {
  return s.match(/\s+|[A-Za-z0-9_]+|[^\sA-Za-z0-9_]/g) ?? []
}

/**
 * Longest-common-subsequence length table, filled from the bottom-right so the
 * forward walk in the diff functions can greedily prefer matches. `eq` is any
 * equivalence relation (used to make matching whitespace-insensitive).
 */
function lcsTable<T>(a: T[], b: T[], eq: (x: T, y: T) => boolean): number[][] {
  const n = a.length
  const m = b.length
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = eq(a[i], b[j]) ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  return dp
}

/** Line-level diff between two documents. Segments are left null here. */
export function diffLines(oldText: string, newText: string, opts?: DiffOptions): RichLine[] {
  const a = splitLines(oldText)
  const b = splitLines(newText)
  // When ignoring whitespace, two lines match if their normalized forms agree;
  // the displayed text for a matched line is always the *new* one.
  const eq = opts?.ignoreWhitespace
    ? (x: string, y: string) => x === y || normalizeLine(x) === normalizeLine(y)
    : (x: string, y: string) => x === y
  const dp = lcsTable(a, b, eq)
  const out: RichLine[] = []
  let i = 0
  let j = 0
  let oldNum = 1
  let newNum = 1
  while (i < a.length && j < b.length) {
    if (eq(a[i], b[j])) {
      out.push({ type: 'equal', text: b[j], oldNumber: oldNum++, newNumber: newNum++, segments: null })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ type: 'delete', text: a[i], oldNumber: oldNum++, newNumber: null, segments: null })
      i++
    } else {
      out.push({ type: 'insert', text: b[j], oldNumber: null, newNumber: newNum++, segments: null })
      j++
    }
  }
  while (i < a.length) {
    out.push({ type: 'delete', text: a[i], oldNumber: oldNum++, newNumber: null, segments: null })
    i++
  }
  while (j < b.length) {
    out.push({ type: 'insert', text: b[j], oldNumber: null, newNumber: newNum++, segments: null })
    j++
  }
  return out
}

function pushSegment(segs: Segment[], text: string, changed: boolean): void {
  const last = segs[segs.length - 1]
  if (last && last.changed === changed) last.text += text
  else segs.push({ text, changed })
}

/**
 * Token-level diff between two single lines. Returns the segment lists for each
 * side plus `common`, the number of characters the two lines share — used to
 * decide whether intra-line highlighting is meaningful.
 */
export function diffWords(
  oldLine: string,
  newLine: string,
  opts?: DiffOptions,
): { old: Segment[]; new: Segment[]; common: number } {
  const a = tokenize(oldLine)
  const b = tokenize(newLine)
  // With ignoreWhitespace, any whitespace token matches any other so pure
  // spacing changes inside an otherwise-changed line aren't highlighted.
  const eq = opts?.ignoreWhitespace
    ? (x: string, y: string) => x === y || (/^\s/.test(x) && /^\s/.test(y))
    : (x: string, y: string) => x === y
  const dp = lcsTable(a, b, eq)
  const oldSegs: Segment[] = []
  const newSegs: Segment[] = []
  let i = 0
  let j = 0
  let common = 0
  while (i < a.length && j < b.length) {
    if (eq(a[i], b[j])) {
      pushSegment(oldSegs, a[i], false)
      pushSegment(newSegs, b[j], false)
      common += a[i].length
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      pushSegment(oldSegs, a[i], true)
      i++
    } else {
      pushSegment(newSegs, b[j], true)
      j++
    }
  }
  while (i < a.length) pushSegment(oldSegs, a[i++], true)
  while (j < b.length) pushSegment(newSegs, b[j++], true)
  return { old: oldSegs, new: newSegs, common }
}

// Below this ratio of shared characters, two paired lines are treated as a
// wholesale rewrite — highlighting every token would just be noise, so we leave
// segments null and render them as plain add/delete lines.
const SIMILARITY_THRESHOLD = 0.25

/**
 * Full line diff with word-level segments attached. Within each block of
 * consecutive deletes followed by consecutive inserts (a "replace"), lines are
 * paired by index and given intra-line highlighting when similar enough.
 */
export function buildLineDiff(oldText: string, newText: string, opts?: DiffOptions): RichLine[] {
  const lines = diffLines(oldText, newText, opts)
  let k = 0
  while (k < lines.length) {
    if (lines[k].type !== 'delete') {
      k++
      continue
    }
    const delStart = k
    while (k < lines.length && lines[k].type === 'delete') k++
    const insStart = k
    while (k < lines.length && lines[k].type === 'insert') k++
    const pairs = Math.min(insStart - delStart, k - insStart)
    for (let p = 0; p < pairs; p++) {
      const del = lines[delStart + p]
      const ins = lines[insStart + p]
      const wd = diffWords(del.text, ins.text, opts)
      const maxLen = Math.max(del.text.length, ins.text.length)
      if (maxLen > 0 && wd.common / maxLen >= SIMILARITY_THRESHOLD) {
        del.segments = wd.old
        ins.segments = wd.new
      }
    }
  }
  // Ignoring whitespace also means blank-line churn shouldn't read as a change:
  // a purely whitespace inserted/deleted line is de-emphasised so it neither
  // tints nor anchors a hunk (it collapses into the unchanged context instead).
  if (opts?.ignoreWhitespace) {
    for (const line of lines) {
      if (line.type !== 'equal' && normalizeLine(line.text) === '') line.muted = true
    }
  }
  return lines
}

/**
 * Whether two documents differ, honouring the same whitespace rule as the diff.
 * Used to decide which fields actually changed before rendering a DiffView.
 */
export function documentsDiffer(oldText: string, newText: string, opts?: DiffOptions): boolean {
  if (oldText === newText) return false
  if (!opts?.ignoreWhitespace) return true
  // Drop blank lines too, so adding/removing blank lines alone isn't a change.
  const norm = (s: string) =>
    splitLines(s).map(normalizeLine).filter(l => l !== '').join('\n')
  return norm(oldText) !== norm(newText)
}

/**
 * Align a flat RichLine list into left/right rows for side-by-side rendering.
 * Deletes go left, inserts go right, equals span both; within a replace block
 * deletes and inserts are paired by index (mirroring buildLineDiff's pairing).
 */
export function buildSplitRows(lines: RichLine[]): SplitRow[] {
  const rows: SplitRow[] = []
  let i = 0
  while (i < lines.length) {
    if (lines[i].type === 'equal') {
      rows.push({ left: lines[i], right: lines[i] })
      i++
      continue
    }
    const dels: RichLine[] = []
    const ins: RichLine[] = []
    while (i < lines.length && lines[i].type === 'delete') dels.push(lines[i++])
    while (i < lines.length && lines[i].type === 'insert') ins.push(lines[i++])
    const n = Math.max(dels.length, ins.length)
    for (let k = 0; k < n; k++) {
      rows.push({ left: dels[k] ?? null, right: ins[k] ?? null })
    }
  }
  return rows
}

/**
 * Group a sequence into runs kept around changes and collapsible "gap" runs of
 * unchanged context. Keeps `context` items on either side of each change; only
 * collapses gaps with at least `minGap` items (collapsing one or two lines is
 * pointless). Adjacent kept runs are merged. Generic over both RichLine
 * (unified) and SplitRow (split) via the `isChange` predicate.
 */
export function collapse<T>(
  items: T[],
  isChange: (item: T) => boolean,
  context = 3,
  minGap = 4,
): Group<T>[] {
  const keep = new Array(items.length).fill(false)
  for (let idx = 0; idx < items.length; idx++) {
    if (!isChange(items[idx])) continue
    for (let d = -context; d <= context; d++) {
      const k = idx + d
      if (k >= 0 && k < items.length) keep[k] = true
    }
  }
  const groups: Group<T>[] = []
  let i = 0
  while (i < items.length) {
    const start = i
    const kept = keep[i]
    while (i < items.length && keep[i] === kept) i++
    const slice = items.slice(start, i)
    const asGap = !kept && slice.length >= minGap
    const type: Group<T>['type'] = asGap ? 'gap' : 'lines'
    const last = groups[groups.length - 1]
    if (type === 'lines' && last && last.type === 'lines') last.items.push(...slice)
    else groups.push({ type, items: slice })
  }
  return groups
}
