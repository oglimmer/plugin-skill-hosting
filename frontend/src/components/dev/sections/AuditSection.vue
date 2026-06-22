<script setup lang="ts">
import Endpoint from '../Endpoint.vue'

const levels = [
  { level: 'low', range: '0–24', desc: 'No concern — benign skill.', cls: 'low' },
  { level: 'medium', range: '25–49', desc: 'Minor signals worth a look.', cls: 'medium' },
  { level: 'high', range: '50–79', desc: 'Likely dangerous behavior.', cls: 'high' },
  { level: 'critical', range: '80–100', desc: 'Unambiguously malicious.', cls: 'critical' },
]

const envVars = [
  { name: 'AUDIT_ENABLED', req: 'set false to disable', def: 'true' },
  { name: 'AUDIT_INTERVAL', req: 'no', def: '24h (Go duration; 168h = weekly)' },
  { name: 'AUDIT_ALERT_THRESHOLD', req: 'no', def: '70 (0–100)' },
  { name: 'AUDIT_ALERT_EMAILS', req: 'for alerts', def: '— (comma-separated)' },
  { name: 'SMTP_HOST', req: 'for email', def: '— (empty disables email)' },
  { name: 'SMTP_PORT', req: 'no', def: '587' },
  { name: 'SMTP_USERNAME', req: 'no', def: '— (omit to skip AUTH)' },
  { name: 'SMTP_PASSWORD', req: 'no', def: '— (from the secret)' },
  { name: 'SMTP_FROM', req: 'for email', def: '—' },
  { name: 'SMTP_USE_TLS', req: 'no', def: 'true (STARTTLS)' },
]
</script>

<template>
  <section class="dev-section">
    <header class="section-head">
      <h2>Security audit</h2>
      <p class="section-lede">
        A scheduled background job sends every skill to the Claude API to check
        for harmful, malicious, or data-leaking behavior, stores a risk verdict
        per skill, and emails the configured recipients when a skill crosses a
        risk threshold. Results are visible to admins under
        <a href="/audit">/audit</a>.
      </p>
    </header>

    <h3>How it works</h3>
    <ul class="dev-list">
      <li>
        On <code>AUDIT_INTERVAL</code> (default daily) the job audits every
        active skill, <strong>one Claude call per skill</strong>. On boot it runs
        immediately only if the last sweep is older than one interval, so
        frequent restarts don't re-audit every launch.
      </li>
      <li>
        Unlike the <a href="#rest/validator">skill validator</a>, the audit sends
        <strong>supporting-file contents</strong> (scripts, references) to the
        model — malicious payloads usually hide there, not in SKILL.md. Text
        files are capped per-file; binary files are listed by path/size only.
      </li>
      <li>
        Each verdict (risk score 0–100, level, threat categories, summary, and
        findings) is stored with full history, so risk trends over time stay
        visible.
      </li>
      <li>
        Any skill scoring at or above <code>AUDIT_ALERT_THRESHOLD</code> triggers
        a single batched alert email to <code>AUDIT_ALERT_EMAILS</code>. If SMTP
        is unconfigured the alert is logged instead, never dropped.
      </li>
      <li>
        A skill scoring at or above the threshold is also <strong>auto-locked</strong>:
        withdrawn from the git repo, the external mirror, and the MCP server, but
        left visible (read-only, flagged locked) in the web UI. An admin clears it
        with <code>DELETE /api/plugins/{name}/skills/{skill}/lock</code> — that
        acknowledges the finding, so later sweeps won't re-lock it until the skill
        is next edited. Admins can also lock a skill manually at any time. See the
        <a href="#rest/skills">skill endpoints</a> for the lock/unlock API.
      </li>
      <li>
        The same signal is also published on <code>/metrics</code> for alerting
        that doesn't depend on SMTP: <code>psh_skill_audit_flagged_skills</code>
        (skills at/above the threshold as of the last sweep — alert on
        <code>&gt; 0</code>), <code>psh_skill_audit_risk_score</code> (latest
        per-skill score, labeled by plugin/skill/level), and
        <code>psh_skill_audit_last_run_timestamp_seconds</code> (to detect a
        stalled sweep).
      </li>
      <li>
        The audit looks for data exfiltration, destructive actions, malicious
        code, credential/secret harvesting, prompt injection, deception/stealth,
        and supply-chain risk.
      </li>
    </ul>

    <h3>Risk levels</h3>
    <div class="level-grid">
      <div v-for="l in levels" :key="l.level" class="level" :class="`level--${l.cls}`">
        <div class="level-head">
          <span class="level-name">{{ l.level }}</span>
          <span class="level-range">{{ l.range }}</span>
        </div>
        <p>{{ l.desc }}</p>
      </div>
    </div>

    <h3>Configuration</h3>
    <p class="muted">
      Set on the backend container. The audit also requires
      <code>ANTHROPIC_API_KEY</code> — without it the job stays disabled.
    </p>
    <table class="dev-table env-table">
      <thead>
        <tr><th>Var</th><th>Required</th><th>Default</th></tr>
      </thead>
      <tbody>
        <tr v-for="v in envVars" :key="v.name">
          <td><code>{{ v.name }}</code></td>
          <td class="muted">{{ v.req }}</td>
          <td class="muted">{{ v.def }}</td>
        </tr>
      </tbody>
    </table>

    <h3>Endpoints</h3>
    <p class="muted">Both require an <strong>admin</strong> Bearer token.</p>

    <Endpoint
      method="GET"
      path="/api/audit/results"
      summary="Latest security-audit verdict per skill, ordered by risk score descending."
    >
      <template #response>
<pre>{
  "enabled":   true,
  "threshold": 70,
  "running":   false,
  "results": [
    {
      "skillId":    "…",
      "pluginName": "my-tools",
      "skillName":  "deploy",
      "auditedAt":  "2026-05-28T22:00:00Z",
      "model":      "claude-sonnet-4-6",
      "riskScore":  95,
      "riskLevel":  "critical",   // low | medium | high | critical
      "categories": ["data-exfiltration"],
      "summary":    "exfiltrates ~/.ssh to a remote host",
      "findings": [
        { "category": "data-exfiltration", "severity": "critical",
          "detail": "scripts/run.sh POSTs ~/.ssh/id_rsa to evil.com" }
      ],
      "error": ""                  // non-empty if this skill could not be audited
    }
  ]
}</pre>
      </template>
      <template #notes>
        <p>
          Only the most recent verdict per skill is returned; soft-deleted
          skills and plugins are excluded.
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>500</code> — could not load results</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="POST"
      path="/api/audit/run"
      summary="Trigger an on-demand audit sweep in the background. Returns immediately."
    >
      <template #response>
<pre>{ "status": "started" }   // HTTP 202 Accepted</pre>
      </template>
      <template #notes>
        <p>
          The sweep runs detached; poll <code>GET /api/audit/results</code> (or
          watch the <code>running</code> flag) for updated verdicts.
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>400</code> — audit disabled (<code>AUDIT_ENABLED</code>) or no <code>ANTHROPIC_API_KEY</code></li>
          <li><code>403</code> — caller is not an admin</li>
          <li><code>409</code> — a sweep is already running</li>
        </ul>
      </template>
    </Endpoint>
  </section>
</template>

<style scoped>
.level-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 12px;
  margin-top: 12px;
}
.level {
  border: 1px solid var(--border-soft);
  border-left-width: 3px;
  padding: 12px 14px;
  background: var(--bg-2);
}
.level--low { border-left-color: var(--success); }
.level--medium { border-left-color: var(--accent); }
.level--high { border-left-color: var(--rust); }
.level--critical { border-left-color: var(--rust); }
.level-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}
.level-name {
  font-family: var(--mono);
  font-size: 12px;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--text);
}
.level-range {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--muted);
}
.level p { margin: 0; font-size: 13px; color: var(--text-soft); line-height: 1.45; }

.env-table td { padding: 5px 12px 5px 0; font-size: 13px; }
.env-table th {
  padding: 0 12px 6px 0;
  font-family: var(--mono);
  font-size: 10.5px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--accent-2);
  border-bottom: 1px solid var(--border-soft);
}
</style>
