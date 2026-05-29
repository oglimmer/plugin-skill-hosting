<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errMsg } from '../api'
import ErrorAlert from '../components/ErrorAlert.vue'
import type { AuditResult } from '../types'

const results = ref<AuditResult[]>([])
const enabled = ref(true)
const threshold = ref(70)
const running = ref(false)
const loading = ref(true)
const error = ref('')
const notice = ref('')
const expanded = ref<Set<string>>(new Set())

const flagged = computed(() => results.value.filter(r => !r.error && r.riskScore >= threshold.value))
const clean = computed(() => results.value.filter(r => !r.error && r.riskScore < threshold.value))
const errored = computed(() => results.value.filter(r => r.error))

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

function toggle(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const resp = await api.listAuditResults()
    results.value = resp.results
    enabled.value = resp.enabled
    threshold.value = resp.threshold
    running.value = resp.running
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

async function runNow() {
  error.value = ''
  notice.value = ''
  try {
    await api.runAudit()
    running.value = true
    notice.value = 'Audit started in the background. Refresh in a minute or two to see updated results.'
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

onMounted(load)
</script>

<template>
  <h1>Security audit</h1>

  <p class="muted intro">
    Each skill is periodically sent to Claude to check for harmful, malicious, or
    data-leaking behavior. Skills with a risk score at or above the alert
    threshold (<strong>{{ threshold }}</strong>) are highlighted and trigger an
    email to the configured recipients.
  </p>

  <div v-if="!enabled" class="card warn">
    The scheduled audit is currently <strong>disabled</strong>. Set
    <code>AUDIT_ENABLED=true</code> (and <code>ANTHROPIC_API_KEY</code>) to enable it.
  </div>

  <div class="toolbar">
    <button type="button" class="secondary" :disabled="running || !enabled" @click="runNow">
      {{ running ? 'Audit running…' : 'Run audit now' }}
    </button>
    <button type="button" class="secondary" @click="load">Refresh</button>
  </div>

  <p v-if="notice" class="muted notice">{{ notice }}</p>

  <div v-if="loading" class="muted">Loading…</div>
  <ErrorAlert v-else-if="error" :message="error" />
  <template v-else>
    <div v-if="results.length === 0" class="card">
      <p class="muted" style="margin: 0">No audit results yet. Run an audit to populate this page.</p>
    </div>

    <section v-if="flagged.length > 0" class="section">
      <h2 class="section-title">
        Flagged
        <span class="chip chip--flag">{{ flagged.length }}</span>
      </h2>
      <div v-for="r in flagged" :key="r.skillId" class="card result" @click="toggle(r.skillId)">
        <div class="result-head">
          <span class="badge" :class="`badge--${r.riskLevel}`">{{ r.riskScore }}</span>
          <div class="result-title">
            <RouterLink
              class="skill-link"
              :to="`/plugins/${r.pluginName}/skills/${r.skillName}/edit`"
              @click.stop
            >{{ r.pluginName }} / {{ r.skillName }}</RouterLink>
            <span class="muted summary">{{ r.summary }}</span>
          </div>
          <small class="muted when">{{ fmt(r.auditedAt) }}</small>
        </div>
        <div v-if="r.categories.length" class="cats">
          <span v-for="c in r.categories" :key="c" class="tag">{{ c }}</span>
        </div>
        <ul v-if="expanded.has(r.skillId) && r.findings.length" class="findings">
          <li v-for="(f, i) in r.findings" :key="i">
            <span class="sev" :class="`sev--${f.severity}`">{{ f.severity }}</span>
            <strong>{{ f.category }}</strong> — {{ f.detail }}
          </li>
        </ul>
      </div>
    </section>

    <section v-if="clean.length > 0" class="section">
      <h2 class="section-title">
        Clean
        <span class="chip">{{ clean.length }}</span>
      </h2>
      <table class="card" style="padding: 0">
        <thead>
          <tr>
            <th style="padding-left: 20px">Skill</th>
            <th>Risk</th>
            <th>Summary</th>
            <th>Audited</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in clean" :key="r.skillId">
            <td style="padding-left: 20px">
              <RouterLink
                class="skill-link"
                :to="`/plugins/${r.pluginName}/skills/${r.skillName}/edit`"
              >{{ r.pluginName }} / {{ r.skillName }}</RouterLink>
            </td>
            <td><span class="badge" :class="`badge--${r.riskLevel}`">{{ r.riskScore }}</span></td>
            <td class="muted">{{ r.summary || '—' }}</td>
            <td class="muted" style="white-space: nowrap"><small>{{ fmt(r.auditedAt) }}</small></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-if="errored.length > 0" class="section">
      <details class="card">
        <summary class="muted" style="cursor: pointer">
          Could not be audited ({{ errored.length }})
        </summary>
        <ul class="errlist">
          <li v-for="r in errored" :key="r.skillId">
            <RouterLink
              class="skill-link"
              :to="`/plugins/${r.pluginName}/skills/${r.skillName}/edit`"
            >{{ r.pluginName }} / {{ r.skillName }}</RouterLink>
            <span class="muted"> — {{ r.error }}</span>
          </li>
        </ul>
      </details>
    </section>
  </template>
</template>

<style scoped>
.intro {
  margin: -14px 0 20px;
  max-width: 70ch;
}
.warn {
  margin-bottom: 16px;
  border-color: rgba(245, 165, 36, 0.5);
}
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
}
.notice {
  margin: -8px 0 16px;
}
.section {
  margin-bottom: 28px;
}
.section-title {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  margin: 0 0 12px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.chip {
  display: inline-grid;
  place-items: center;
  min-width: 22px;
  padding: 0 8px;
  height: 20px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.12em;
  color: var(--muted);
  border: 1px solid var(--border);
  border-radius: 999px;
}
.chip--flag {
  color: #e5484d;
  border-color: rgba(229, 72, 77, 0.5);
}
.result {
  margin-bottom: 10px;
  cursor: pointer;
}
.result-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.result-title {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1;
  min-width: 0;
}
.skill-link {
  color: var(--text);
  font-weight: 600;
  border-bottom: 1px solid var(--accent);
  padding-bottom: 1px;
  text-decoration: none;
  transition: color 0.12s ease;
}
.skill-link:hover { color: var(--accent); }
.summary {
  font-size: 13px;
}
.when {
  white-space: nowrap;
}
.badge {
  display: inline-grid;
  place-items: center;
  min-width: 34px;
  height: 26px;
  padding: 0 6px;
  border-radius: 6px;
  font-family: var(--mono);
  font-weight: 700;
  font-size: 13px;
  color: #fff;
}
.badge--low { background: #30a46c; }
.badge--medium { background: #f5a524; color: #1a1a1a; }
.badge--high { background: #f76808; }
.badge--critical { background: #e5484d; }
.cats {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 10px;
}
.tag {
  font-family: var(--mono);
  font-size: 10.5px;
  padding: 2px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--muted);
}
.findings {
  margin: 12px 0 0;
  padding-left: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.findings li {
  font-size: 13px;
  line-height: 1.5;
}
.sev {
  display: inline-block;
  font-family: var(--mono);
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  padding: 1px 6px;
  border-radius: 4px;
  margin-right: 6px;
  vertical-align: middle;
}
.sev--critical { background: rgba(229, 72, 77, 0.15); color: #e5484d; }
.sev--high { background: rgba(247, 104, 8, 0.15); color: #f76808; }
.sev--medium { background: rgba(245, 165, 36, 0.15); color: #f5a524; }
.sev--low { background: var(--border); color: var(--muted); }
.errlist {
  margin: 12px 0 0;
  padding-left: 18px;
  font-size: 13px;
}
.errlist li {
  margin-bottom: 6px;
}
</style>
