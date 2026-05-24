<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, errMsg } from '../api'
import type { SkillVersion } from '../types'
import ErrorAlert from './ErrorAlert.vue'

const props = defineProps<{
  pluginName: string
  skillName: string | null
}>()

const emit = defineEmits<{
  revert: [version: number]
}>()

const versions = ref<SkillVersion[]>([])
const versionsError = ref('')

async function reload() {
  if (!props.skillName) {
    versions.value = []
    versionsError.value = ''
    return
  }
  versionsError.value = ''
  try {
    versions.value = await api.skillVersions(props.pluginName, props.skillName)
  } catch (e: unknown) {
    versionsError.value = errMsg(e)
  }
}

defineExpose({ reload })

watch(
  () => [props.pluginName, props.skillName] as const,
  reload,
  { immediate: true },
)

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}
</script>

<template>
  <details class="svh">
    <summary class="svh__head">
      <span class="svh__toggle" aria-hidden="true"></span>
      <span class="svh__title">history</span>
      <span v-if="!versionsError && versions.length" class="svh__count">
        {{ versions.length }} version{{ versions.length === 1 ? '' : 's' }}
      </span>
      <span class="spacer"></span>
      <span class="svh__hint" aria-hidden="true">
        <span class="svh__hint-open">expand</span>
        <span class="svh__hint-close">collapse</span>
        <span class="svh__chev">▸</span>
      </span>
    </summary>
    <ErrorAlert :message="versionsError" />
    <p v-if="!versionsError && versions.length === 0" class="svh__empty">no history yet.</p>
    <table v-else class="svh__table">
      <thead>
        <tr>
          <th>v</th>
          <th>action</th>
          <th>by</th>
          <th>when</th>
          <th>description</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="v in versions" :key="v.id">
          <td class="svh__ver">{{ v.version }}</td>
          <td><span class="svh__action" :class="`svh__action--${v.action}`">{{ v.action }}</span></td>
          <td>{{ v.editedByName || '—' }}</td>
          <td class="svh__when">{{ fmt(v.editedAt) }}</td>
          <td class="svh__desc">{{ v.description }}</td>
          <td class="svh__act">
            <button
              v-if="v.action !== 'delete'"
              class="svh__revert"
              type="button"
              @click="emit('revert', v.version)"
            >revert →</button>
          </td>
        </tr>
      </tbody>
    </table>
  </details>
</template>

<style scoped>
.svh {
  margin-top: 14px;
}
.svh__head {
  cursor: pointer;
  list-style: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  background: var(--bg-2);
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  text-transform: uppercase;
  color: var(--text-soft);
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
  user-select: none;
}
.svh__head::-webkit-details-marker { display: none; }
.svh__toggle {
  display: inline-grid;
  place-items: center;
  width: 18px;
  height: 18px;
  border: 1px solid var(--border);
  color: var(--accent);
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0;
  line-height: 1;
  flex: 0 0 auto;
  transition: border-color 0.15s ease;
}
.svh:not([open]) > .svh__head .svh__toggle::before { content: '+'; }
.svh[open] > .svh__head .svh__toggle::before { content: '−'; }
.svh__title { letter-spacing: inherit; flex: 0 0 auto; }
.svh__count {
  font-size: 10.5px;
  color: var(--muted);
  letter-spacing: 0.08em;
  text-transform: lowercase;
  font-weight: 500;
  flex: 0 0 auto;
}
.svh__hint {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--muted);
}
.svh__hint-open,
.svh__hint-close { display: none; }
.svh:not([open]) > .svh__head .svh__hint-open { display: inline; }
.svh[open] > .svh__head .svh__hint-close { display: inline; }
.svh__chev {
  display: inline-block;
  color: var(--accent);
  font-size: 12px;
  transition: transform 0.18s ease;
  letter-spacing: 0;
}
.svh[open] > .svh__head .svh__chev { transform: rotate(90deg); }
.svh__head:hover {
  color: var(--text);
  border-color: var(--accent);
  background: rgba(245, 165, 36, 0.04);
}
.svh__head:hover .svh__toggle { border-color: var(--accent); }
.svh__head:hover .svh__hint { color: var(--text-soft); }
.svh[open] > .svh__head {
  color: var(--text);
  border-bottom-color: var(--accent);
  margin-bottom: 12px;
}
.svh__empty {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--muted);
}

.svh__table {
  width: 100%;
  border-collapse: collapse;
  font-family: var(--mono);
}
.svh__table th {
  text-align: left;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
  background: transparent;
}
.svh__table td {
  padding: 9px 12px;
  border-bottom: 1px solid var(--border-soft);
  font-size: 12.5px;
  color: var(--text);
  vertical-align: top;
}
.svh__table tbody tr:last-child td { border-bottom: 0; }
.svh__table tbody tr:hover { background: rgba(255, 255, 255, 0.02); }

.svh__ver {
  font-weight: 700;
  color: var(--accent-2);
  width: 1%;
  white-space: nowrap;
}
.svh__when {
  color: var(--muted);
  font-size: 11.5px;
  white-space: nowrap;
}
.svh__desc {
  color: var(--text-soft);
  word-break: break-word;
}
.svh__act {
  text-align: right;
  width: 1%;
  white-space: nowrap;
}

.svh__action {
  display: inline-block;
  padding: 1px 7px;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.14em;
  text-transform: lowercase;
  border: 1px solid var(--border);
  color: var(--text-soft);
  background: transparent;
}
.svh__action--create { border-color: var(--accent); color: var(--accent-2); }
.svh__action--update { border-color: var(--border); color: var(--text-soft); }
.svh__action--delete { border-color: var(--rust); color: var(--rust); }
.svh__action--revert { border-color: var(--border); color: var(--muted); }

.svh__revert {
  background: transparent;
  border: 0;
  color: var(--text-soft);
  padding: 0;
  margin: 0;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.04em;
  text-transform: lowercase;
  font-weight: 500;
  cursor: pointer;
  transition: color 0.12s ease;
}
.svh__revert::before { display: none; content: none; }
.svh__revert:hover { color: var(--accent); background: transparent; transform: none; }
</style>
