<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, errMsg } from '../api'
import type { SkillVersion } from '../types'
import { documentsDiffer } from '../lib/diff'
import DiffView from '../components/DiffView.vue'
import ErrorAlert from '../components/ErrorAlert.vue'

const props = defineProps<{
  pluginName: string
  skillName: string
  base: number | null
  target: number | null
}>()

const router = useRouter()

const versions = ref<SkillVersion[]>([])
const loading = ref(true)
const error = ref('')

// Side-by-side reads best on a wide screen; fall back to inline on narrow ones.
const mode = ref<'split' | 'unified'>(window.innerWidth < 920 ? 'unified' : 'split')
const ignoreWhitespace = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    versions.value = await api.skillVersions(props.pluginName, props.skillName)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => [props.pluginName, props.skillName], load)

const versionsDesc = computed(() => [...versions.value].sort((a, b) => b.version - a.version))
const latest = computed(() => versionsDesc.value[0]?.version ?? 0)

// Largest version strictly older than `v`, or 0 (the "empty" sentinel) when `v`
// is the first version — so the create/v1 diff shows the whole skill as added.
function predecessorOf(v: number): number {
  let best = 0
  for (const ver of versions.value) {
    if (ver.version < v && ver.version > best) best = ver.version
  }
  return best
}

// Effective selections: fall back to "latest vs its predecessor" when the URL
// carries no explicit base/target.
const targetVer = computed(() => props.target ?? latest.value)
const baseVer = computed(() => props.base ?? predecessorOf(targetVer.value))

function navigate(base: number, target: number) {
  router.replace({ query: { base: String(base), target: String(target) } })
}

const baseModel = computed<number>({
  get: () => baseVer.value,
  set: v => navigate(Number(v), targetVer.value),
})
const targetModel = computed<number>({
  get: () => targetVer.value,
  set: v => navigate(baseVer.value, Number(v)),
})

function swap() {
  navigate(targetVer.value, baseVer.value)
}

interface Snapshot {
  version: number
  name: string
  description: string
  body: string
  extraFrontmatter: string
}

// Version 0 is the synthetic "empty" snapshot (before the skill existed).
function snapshotFor(ver: number): Snapshot | null {
  if (ver === 0) return { version: 0, name: '', description: '', body: '', extraFrontmatter: '' }
  const v = versions.value.find(x => x.version === ver)
  if (!v) return null
  return {
    version: v.version,
    name: v.name,
    description: v.description,
    body: v.body,
    extraFrontmatter: v.extraFrontmatter ?? '',
  }
}

const baseSnap = computed(() => snapshotFor(baseVer.value))
const targetSnap = computed(() => snapshotFor(targetVer.value))

const FIELDS = [
  { key: 'name', label: 'name' },
  { key: 'description', label: 'description' },
  { key: 'extraFrontmatter', label: 'extra frontmatter' },
  { key: 'body', label: 'body' },
] as const

const changedFields = computed(() => {
  const b = baseSnap.value
  const t = targetSnap.value
  if (!b || !t) return []
  return FIELDS.filter(f => documentsDiffer(b[f.key], t[f.key], { ignoreWhitespace: ignoreWhitespace.value }))
})

const editLink = computed(
  () => `/plugins/${props.pluginName}/skills/${props.skillName}/edit`,
)

function fmtDate(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

// Option label: "v3 · update · Jan 4, 2026"
function optionLabel(v: SkillVersion) {
  return `v${v.version} · ${v.action} · ${fmtDate(v.editedAt)}`
}
</script>

<template>
  <div class="cmp">
    <header class="cmp-bar">
      <div class="cmp-bar__id">
        <span class="cmp-bar__kind">DIFF</span>
        <span class="cmp-bar__divider"></span>
        <code class="cmp-bar__path">
          {{ pluginName }}/<span class="cmp-bar__leaf">{{ skillName }}</span>
        </code>
      </div>
      <RouterLink :to="editLink" class="cmp-btn cmp-btn--ghost">← back to editor</RouterLink>
    </header>

    <p v-if="loading" class="cmp-loading">loading history…</p>
    <ErrorAlert v-else :message="error" />

    <template v-if="!loading && !error">
      <!-- Version pickers + view toggle -->
      <div class="cmp-controls">
        <div class="cmp-pick">
          <label class="cmp-pick__label">base</label>
          <select v-model="baseModel" class="cmp-select">
            <option :value="0">∅ empty</option>
            <option v-for="v in versionsDesc" :key="v.id" :value="v.version">
              {{ optionLabel(v) }}
            </option>
          </select>
        </div>

        <button type="button" class="cmp-swap" title="Swap base and target" @click="swap">⇄</button>

        <div class="cmp-pick">
          <label class="cmp-pick__label">target</label>
          <select v-model="targetModel" class="cmp-select">
            <option :value="0">∅ empty</option>
            <option v-for="v in versionsDesc" :key="v.id" :value="v.version">
              {{ optionLabel(v) }}
            </option>
          </select>
        </div>

        <span class="spacer"></span>

        <label class="cmp-ws">
          <input v-model="ignoreWhitespace" type="checkbox" class="cmp-ws__box" />
          ignore whitespace
        </label>

        <div class="cmp-toggle" role="group" aria-label="Diff view mode">
          <button
            type="button"
            class="cmp-toggle__btn"
            :class="{ 'cmp-toggle__btn--active': mode === 'split' }"
            :aria-pressed="mode === 'split'"
            @click="mode = 'split'"
          >split</button>
          <button
            type="button"
            class="cmp-toggle__btn"
            :class="{ 'cmp-toggle__btn--active': mode === 'unified' }"
            :aria-pressed="mode === 'unified'"
            @click="mode = 'unified'"
          >unified</button>
        </div>
      </div>

      <!-- Direction summary -->
      <p v-if="baseSnap && targetSnap" class="cmp-summary">
        comparing
        <span class="cmp-summary__from">{{ baseVer === 0 ? '∅ empty' : `v${baseVer}` }}</span>
        →
        <span class="cmp-summary__to">{{ targetVer === 0 ? '∅ empty' : `v${targetVer}` }}</span>
        <template v-if="changedFields.length">
          · changed:
          <span v-for="f in changedFields" :key="f.key" class="cmp-chip">{{ f.label }}</span>
        </template>
      </p>

      <!-- States -->
      <p v-if="!baseSnap || !targetSnap" class="cmp-missing">
        one of the selected versions could not be found.
      </p>
      <p v-else-if="!changedFields.length" class="cmp-identical">
        these two versions are identical.
      </p>

      <!-- Field diffs -->
      <section
        v-for="f in changedFields"
        v-else
        :key="f.key"
        class="cmp-field"
      >
        <header class="cmp-field__head">
          <span class="cmp-field__label">{{ f.label }}</span>
        </header>
        <DiffView
          :old-text="baseSnap![f.key]"
          :new-text="targetSnap![f.key]"
          :mode="mode"
          :ignore-whitespace="ignoreWhitespace"
        />
      </section>
    </template>
  </div>
</template>

<style scoped>
.cmp { margin-top: -16px; }

/* ── Top bar ─────────────────────────────────────────────────────── */
.cmp-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  padding: 14px 16px;
  margin: 0 -16px 0;
  background: var(--bg);
  border-top: 1px solid var(--border-soft);
  border-bottom: 1px solid var(--border);
}
.cmp-bar__id {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1 1 auto;
}
.cmp-bar__kind {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  color: var(--accent);
  padding: 3px 8px;
  border: 1px solid var(--accent);
}
.cmp-bar__divider { width: 1px; height: 16px; background: var(--border); }
.cmp-bar__path {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--text-soft);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.cmp-bar__leaf { color: var(--text); }

/* ── Flat buttons (match SkillEditView) ──────────────────────────── */
.cmp-btn {
  display: inline-flex;
  align-items: center;
  background: transparent;
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 6px 12px;
  font-family: var(--mono);
  font-size: 11.5px;
  font-weight: 500;
  letter-spacing: 0.02em;
  text-transform: lowercase;
  line-height: 1.5;
  cursor: pointer;
  transition: border-color 0.12s ease, color 0.12s ease, background 0.12s ease;
}
.cmp-btn:hover { color: var(--accent); border-color: var(--accent); }
.cmp-btn--ghost { border-color: transparent; color: var(--text-soft); }
.cmp-btn--ghost:hover { color: var(--accent); border-color: var(--accent); }

.cmp-loading {
  margin: 20px 0 0;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
}

/* ── Controls row ────────────────────────────────────────────────── */
.cmp-controls {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 22px;
}
.cmp-pick {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}
.cmp-pick__label {
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
}
.cmp-select {
  background: var(--bg-2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 7px 10px;
  font-family: var(--mono);
  font-size: 12.5px;
  outline: none;
  cursor: pointer;
  transition: border-color 0.15s ease;
}
.cmp-select:focus { border-color: var(--accent); }

.cmp-swap {
  background: transparent;
  color: var(--text-soft);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 7px 11px;
  margin-bottom: 0;
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  transition: color 0.12s ease, border-color 0.12s ease, background 0.12s ease;
}
.cmp-swap::before { display: none; content: none; }
.cmp-swap:hover { color: var(--accent); border-color: var(--accent); background: transparent; transform: none; }

.cmp-ws {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-family: var(--mono);
  font-size: 11.5px;
  letter-spacing: 0.02em;
  color: var(--text-soft);
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.cmp-ws__box {
  width: 14px;
  height: 14px;
  margin: 0;
  cursor: pointer;
  accent-color: var(--accent);
}
.cmp-ws:hover { color: var(--text); }

.cmp-toggle {
  display: inline-flex;
  border: 1px solid var(--border);
}
.cmp-toggle__btn {
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-radius: 0;
  padding: 7px 14px;
  font-family: var(--mono);
  font-size: 11.5px;
  letter-spacing: 0.04em;
  text-transform: lowercase;
  cursor: pointer;
  transition: color 0.12s ease, background 0.12s ease;
}
.cmp-toggle__btn::before { display: none; content: none; }
.cmp-toggle__btn:hover { color: var(--text); background: transparent; transform: none; }
.cmp-toggle__btn + .cmp-toggle__btn { border-left: 1px solid var(--border); }
.cmp-toggle__btn--active,
.cmp-toggle__btn--active:hover {
  color: var(--bg);
  background: var(--accent);
  font-weight: 700;
}

/* ── Summary line ────────────────────────────────────────────────── */
.cmp-summary {
  margin: 18px 0 0;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
  letter-spacing: 0.02em;
}
.cmp-summary__from { color: var(--rust); font-weight: 600; }
.cmp-summary__to { color: var(--success); font-weight: 600; }
.cmp-chip {
  display: inline-block;
  margin-left: 4px;
  padding: 1px 7px;
  border: 1px solid var(--border);
  color: var(--text-soft);
  font-size: 11px;
}

.cmp-missing,
.cmp-identical {
  margin: 28px 0;
  padding: 24px;
  text-align: center;
  font-family: var(--mono);
  font-size: 13px;
  color: var(--muted);
  border: 1px dashed var(--border);
  background: var(--bg-2);
}

/* ── Field section ───────────────────────────────────────────────── */
.cmp-field { margin-top: 22px; }
.cmp-field__head {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}
.cmp-field__label {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}

@media (max-width: 720px) {
  .cmp-controls { gap: 8px; }
  .cmp-select { font-size: 11.5px; }
}
</style>
