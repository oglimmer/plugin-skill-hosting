<script setup lang="ts">
import { computed, ref } from 'vue'
import { buildLineDiff, buildSplitRows, collapse, isChangeLine, type RichLine } from '../lib/diff'

const props = defineProps<{
  oldText: string
  newText: string
  mode: 'split' | 'unified'
  ignoreWhitespace?: boolean
}>()

const NBSP = ' '

const richLines = computed(() =>
  buildLineDiff(props.oldText, props.newText, { ignoreWhitespace: props.ignoreWhitespace }),
)
const hasChanges = computed(() => richLines.value.some(isChangeLine))

// Gaps the user has chosen to expand, keyed `${mode}:${groupIndex}`. Reassigned
// (not mutated) so Vue tracks the change.
const expanded = ref<Set<string>>(new Set())
function toggle(key: string) {
  const next = new Set(expanded.value)
  next.add(key)
  expanded.value = next
}

type UnifiedItem =
  | { kind: 'gap'; key: string; count: number }
  | { kind: 'line'; key: string; line: RichLine }

const unifiedItems = computed<UnifiedItem[]>(() => {
  const groups = collapse(richLines.value, isChangeLine)
  const out: UnifiedItem[] = []
  groups.forEach((g, gi) => {
    const key = `unified:${gi}`
    if (g.type === 'gap' && !expanded.value.has(key)) {
      out.push({ kind: 'gap', key, count: g.items.length })
    } else {
      g.items.forEach((line, li) => out.push({ kind: 'line', key: `${key}:${li}`, line }))
    }
  })
  return out
})

type SplitItem =
  | { kind: 'gap'; key: string; count: number }
  | { kind: 'row'; key: string; left: RichLine | null; right: RichLine | null }

const splitItems = computed<SplitItem[]>(() => {
  const rows = buildSplitRows(richLines.value)
  const groups = collapse(rows, r => isChangeLine(r.left) || isChangeLine(r.right))
  const out: SplitItem[] = []
  groups.forEach((g, gi) => {
    const key = `split:${gi}`
    if (g.type === 'gap' && !expanded.value.has(key)) {
      out.push({ kind: 'gap', key, count: g.items.length })
    } else {
      g.items.forEach((row, li) =>
        out.push({ kind: 'row', key: `${key}:${li}`, left: row.left, right: row.right }),
      )
    }
  })
  return out
})

// Tint for a unified row. Muted (whitespace-only) and equal lines read as
// neutral context.
const rowMod = (l: RichLine | null) =>
  !isChangeLine(l) ? '' : l!.type === 'insert' ? 'dv-row--ins' : 'dv-row--del'

// Tint for one side of a split row. The empty (null) side is striped only when
// it sits opposite a real change — not opposite a muted line.
const sideClass = (l: RichLine | null, other: RichLine | null) => {
  if (!l) return isChangeLine(other) ? 'dv-row--empty' : ''
  return rowMod(l)
}

const sign = (l: RichLine | null) =>
  !isChangeLine(l) ? NBSP : l!.type === 'insert' ? '+' : '−'
</script>

<template>
  <div class="dv">
    <p v-if="!hasChanges" class="dv__same">no changes</p>

    <!-- ── Unified ───────────────────────────────────────────────── -->
    <table v-else-if="mode === 'unified'" class="dv__table dv__table--unified">
      <tbody>
        <template v-for="item in unifiedItems" :key="item.key">
          <tr v-if="item.kind === 'gap'" class="dv-gap">
            <td colspan="3">
              <button type="button" class="dv-gap__btn" @click="toggle(item.key)">
                ⋯ {{ item.count }} unchanged line{{ item.count === 1 ? '' : 's' }} ⋯
              </button>
            </td>
          </tr>
          <tr v-else class="dv-row" :class="rowMod(item.line)">
            <td class="dv__num">{{ item.line.oldNumber ?? '' }}</td>
            <td class="dv__num">{{ item.line.newNumber ?? '' }}</td>
            <td class="dv__code">
              <span class="dv__sign">{{ sign(item.line) }}</span><span
                v-if="item.line.segments"
                class="dv__text"
              ><span
                v-for="(seg, si) in item.line.segments"
                :key="si"
                :class="{ 'dv__seg': seg.changed }"
              >{{ seg.text }}</span></span><span v-else class="dv__text">{{ item.line.text || NBSP }}</span>
            </td>
          </tr>
        </template>
      </tbody>
    </table>

    <!-- ── Split (side-by-side) ──────────────────────────────────── -->
    <table v-else class="dv__table dv__table--split">
      <colgroup>
        <col class="dv__col-num" />
        <col class="dv__col-code" />
        <col class="dv__col-num" />
        <col class="dv__col-code" />
      </colgroup>
      <tbody>
        <template v-for="item in splitItems" :key="item.key">
          <tr v-if="item.kind === 'gap'" class="dv-gap">
            <td colspan="4">
              <button type="button" class="dv-gap__btn" @click="toggle(item.key)">
                ⋯ {{ item.count }} unchanged line{{ item.count === 1 ? '' : 's' }} ⋯
              </button>
            </td>
          </tr>
          <tr v-else class="dv-row dv-row--split">
            <td class="dv__num">{{ item.left?.oldNumber ?? '' }}</td>
            <td class="dv__code dv__code--side" :class="sideClass(item.left, item.right)">
              <template v-if="item.left">
                <span v-if="item.left.segments" class="dv__text"><span
                  v-for="(seg, si) in item.left.segments"
                  :key="si"
                  :class="{ 'dv__seg': seg.changed }"
                >{{ seg.text }}</span></span><span v-else class="dv__text">{{ item.left.text || NBSP }}</span>
              </template>
              <span v-else class="dv__text">{{ NBSP }}</span>
            </td>
            <td class="dv__num">{{ item.right?.newNumber ?? '' }}</td>
            <td class="dv__code dv__code--side" :class="sideClass(item.right, item.left)">
              <template v-if="item.right">
                <span v-if="item.right.segments" class="dv__text"><span
                  v-for="(seg, si) in item.right.segments"
                  :key="si"
                  :class="{ 'dv__seg': seg.changed }"
                >{{ seg.text }}</span></span><span v-else class="dv__text">{{ item.right.text || NBSP }}</span>
              </template>
              <span v-else class="dv__text">{{ NBSP }}</span>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.dv {
  border: 1px solid var(--border);
  background: var(--bg);
  overflow-x: auto;
}
.dv__same {
  margin: 0;
  padding: 14px 16px;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
  letter-spacing: 0.04em;
}

.dv__table {
  width: 100%;
  border-collapse: collapse;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.55;
}
.dv__table--split { table-layout: fixed; }
.dv__col-num { width: 46px; }
.dv__col-code { width: calc(50% - 46px); }

/* Line-number gutters */
.dv__num {
  width: 46px;
  min-width: 46px;
  padding: 0 8px;
  text-align: right;
  vertical-align: top;
  color: var(--muted);
  background: var(--bg-2);
  font-size: 11px;
  border-right: 1px solid var(--border-soft);
  user-select: none;
  white-space: nowrap;
}

/* Code cells */
.dv__code {
  padding: 1px 0;
  vertical-align: top;
  color: var(--text);
}
.dv__code--side {
  padding-left: 6px;
  padding-right: 10px;
  border-right: 1px solid var(--border-soft);
}
.dv__sign {
  display: inline-block;
  width: 1.6em;
  padding-left: 8px;
  color: var(--muted);
  user-select: none;
}
.dv__text {
  white-space: pre-wrap;
  word-break: break-word;
  overflow-wrap: anywhere;
}

/* Row tints */
.dv-row--ins { background: rgba(10, 143, 94, 0.08); }
.dv-row--del { background: rgba(194, 73, 31, 0.08); }
.dv-row--ins .dv__sign { color: var(--success); }
.dv-row--del .dv__sign { color: var(--rust); }

/* Split cells tint per side */
.dv__code--side.dv-row--ins { background: rgba(10, 143, 94, 0.08); }
.dv__code--side.dv-row--del { background: rgba(194, 73, 31, 0.08); }
.dv__code--side.dv-row--empty { background: repeating-linear-gradient(
  45deg,
  transparent,
  transparent 6px,
  rgba(0, 1, 97, 0.025) 6px,
  rgba(0, 1, 97, 0.025) 12px
); }

/* Word-level highlights — stronger than the row tint */
.dv-row--ins .dv__seg,
.dv__code--side.dv-row--ins .dv__seg {
  background: rgba(10, 143, 94, 0.26);
  border-radius: 2px;
}
.dv-row--del .dv__seg,
.dv__code--side.dv-row--del .dv__seg {
  background: rgba(194, 73, 31, 0.24);
  border-radius: 2px;
}

/* Collapsed-context band */
.dv-gap td { padding: 0; border-top: 1px solid var(--border-soft); border-bottom: 1px solid var(--border-soft); }
.dv-gap__btn {
  display: block;
  width: 100%;
  background: var(--bg-2);
  border: 0;
  border-radius: 0;
  margin: 0;
  padding: 5px 12px;
  font-family: var(--mono);
  font-size: 10.5px;
  letter-spacing: 0.12em;
  color: var(--muted);
  cursor: pointer;
  text-align: center;
  text-transform: none;
  transition: color 0.12s ease, background 0.12s ease;
}
.dv-gap__btn::before { display: none; content: none; }
.dv-gap__btn:hover {
  color: var(--accent-2);
  background: rgba(245, 165, 36, 0.07);
  transform: none;
}
</style>
