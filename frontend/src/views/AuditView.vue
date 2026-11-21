<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errMsg } from '../api'
import ErrorAlert from '../components/ErrorAlert.vue'
import type { AuditPendingItem, AuditResult } from '../types'

const results = ref<AuditResult[]>([])
// Audit gate: changes saved but not published yet (checking or held).
const gate = ref(false)
const pending = ref<AuditPendingItem[]>([])
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

// The audit covers skills and agents. These three helpers hide the difference
// from the template: a stable key, the display label, and the edit link.
function itemKey(r: AuditResult): string {
  return r.kind === 'agent' ? `agent:${r.agentId}` : `skill:${r.skillId}`
}
function itemLabel(r: AuditResult): string {
  return r.kind === 'agent'
    ? `${r.pluginName} / agent ${r.agentName}`
    : `${r.pluginName} / ${r.skillName}`
}
function itemLink(r: AuditResult): string {
  return r.kind === 'agent'
    ? `/plugins/${r.pluginName}/agents/${r.agentName}/edit`
    : `/plugins/${r.pluginName}/skills/${r.skillName}/edit`
}

function pendingLink(it: AuditPendingItem): string {
  return it.kind === 'agent'
    ? `/plugins/${it.pluginName}/agents/${it.name}/edit`
    : `/plugins/${it.pluginName}/skills/${it.name}/edit`
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
    gate.value = !!resp.gate
    pending.value = resp.pending ?? []
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

  <p class="muted intro">
    <template v-if="gate">
      <strong>Publish gate: on.</strong> A new or changed skill or agent is
      published only after its security check passes. Until then the last
      published version stays live.
    </template>
    <template v-else>
      <strong>Publish gate: off.</strong> Changes are published at once and
      checked afterwards, if at all. Set <code>AUDIT_ON_CHANGE=true</code> (and
      <code>ANTHROPIC_API_KEY</code>) to check every change before it is published.
    </template>
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

    <section v-if="pending.length > 0" class="section">
      <h2 class="section-title">
        Waiting to publish
        <span class="chip">{{ pending.length }}</span>
      </h2>
      <p class="muted" style="margin: 0 0 10px">
        These changes are saved but not published: each one goes live only after
        its security check passes. A change whose check failed is held — the check
        is retried automatically, and you can publish it by hand on its plugin page.
      </p>
      <table class="card" style="padding: 0">
        <thead>
          <tr>
            <th style="padding-left: 20px">Skill / agent</th>
            <th>State</th>
            <th>Since</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="it in pending" :key="`${it.kind}:${it.pluginName}/${it.name}`">
            <td style="padding-left: 20px">
              <RouterLink class="skill-link" :to="pendingLink(it)">
                {{ it.pluginName }} / {{ it.kind === 'agent' ? 'agent ' : '' }}{{ it.name }}
              </RouterLink>
            </td>
            <td :class="{ muted: !it.error }">
              {{ it.error ? `held — check failed: ${it.error}` : 'checking' }}
            </td>
            <td class="muted" style="white-space: nowrap"><small>{{ fmt(it.since) }}</small></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-if="flagged.length > 0" class="section">
      <h2 class="section-title">
        Flagged
        <span class="chip chip--flag">{{ flagged.length }}</span>
      </h2>
      <div v-for="r in flagged" :key="itemKey(r)" class="card result" @click="toggle(itemKey(r))">
        <div class="result-head">
          <span class="badge" :class="`badge--${r.riskLevel}`">{{ r.riskScore }}</span>
          <div class="result-title">
            <RouterLink
              class="skill-link"
              :to="itemLink(r)"
              @click.stop
            >{{ itemLabel(r) }}</RouterLink>
            <span class="muted summary">{{ r.summary }}</span>
          </div>
          <small class="muted when">{{ fmt(r.auditedAt) }}</small>
        </div>
        <div v-if="r.categories.length" class="cats">
          <span v-for="c in r.categories" :key="c" class="tag">{{ c }}</span>
        </div>
        <ul v-if="expanded.has(itemKey(r)) && r.findings.length" class="findings">
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
            <th style="padding-left: 20px">Skill / agent</th>
            <th>Risk</th>
            <th>Summary</th>
            <th>Audited</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in clean" :key="itemKey(r)">
            <td style="padding-left: 20px">
              <RouterLink
                class="skill-link"
                :to="itemLink(r)"
              >{{ itemLabel(r) }}</RouterLink>
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
          <li v-for="r in errored" :key="itemKey(r)">
            <RouterLink
              class="skill-link"
              :to="itemLink(r)"
            >{{ itemLabel(r) }}</RouterLink>
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
  border-color: rgb(var(--accent-rgb) / 0.5);
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
  color: var(--sev-critical);
  border-color: rgb(var(--sev-critical-rgb) / 0.5);
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
.badge--low { background: var(--sev-low); }
.badge--medium { background: var(--sev-medium); color: #1a1a1a; }
.badge--high { background: var(--sev-high); }
.badge--critical { background: var(--sev-critical); }
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
.sev--critical { background: rgb(var(--sev-critical-rgb) / 0.15); color: var(--sev-critical); }
.sev--high { background: rgb(var(--sev-high-rgb) / 0.15); color: var(--sev-high); }
.sev--medium { background: rgb(var(--sev-medium-rgb) / 0.15); color: var(--sev-medium); }
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
