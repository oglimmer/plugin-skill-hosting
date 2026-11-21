<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import OverviewSection from '../components/dev/sections/OverviewSection.vue'
import AuthSection from '../components/dev/sections/AuthSection.vue'
import RestAuthSection from '../components/dev/sections/RestAuthSection.vue'
import RestAccountSection from '../components/dev/sections/RestAccountSection.vue'
import RestPluginsSection from '../components/dev/sections/RestPluginsSection.vue'
import RestSkillsSection from '../components/dev/sections/RestSkillsSection.vue'
import RestFilesSection from '../components/dev/sections/RestFilesSection.vue'
import RestAgentsSection from '../components/dev/sections/RestAgentsSection.vue'
import RestValidatorSection from '../components/dev/sections/RestValidatorSection.vue'
import MarketplaceSection from '../components/dev/sections/MarketplaceSection.vue'
import GitSection from '../components/dev/sections/GitSection.vue'
import McpSection from '../components/dev/sections/McpSection.vue'
import CliSection from '../components/dev/sections/CliSection.vue'
import AuditSection from '../components/dev/sections/AuditSection.vue'
import ErrorsSection from '../components/dev/sections/ErrorsSection.vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const isAdmin = computed(() => !!auth.user?.isAdmin)

type SectionId =
  | 'overview' | 'auth' | 'rest'
  | 'marketplace' | 'git' | 'mcp' | 'cli' | 'audit' | 'errors'
type RestTabId = 'auth' | 'account' | 'plugins' | 'skills' | 'files' | 'agents' | 'validator'

// The Security audit section documents an admin-only feature, so it is only
// listed (and reachable) for admins. Everything else is public reference.
const allSections: { id: SectionId; title: string; hint: string; adminOnly?: boolean }[] = [
  { id: 'overview',    title: 'Overview',        hint: 'What this server exposes' },
  { id: 'auth',        title: 'Authentication',  hint: 'JWT, API token, HTTP Basic' },
  { id: 'rest',        title: 'REST API',        hint: 'All /api/* endpoints' },
  { id: 'marketplace', title: 'Marketplace',     hint: '/marketplace.json feed' },
  { id: 'git',         title: 'Git',             hint: 'Clone over Smart HTTP' },
  { id: 'mcp',         title: 'MCP server',      hint: 'Tools at /mcp' },
  { id: 'cli',         title: 'CLI',             hint: 'Import a plugin from disk' },
  { id: 'audit',       title: 'Security audit',  hint: 'Scheduled skill threat scan', adminOnly: true },
  { id: 'errors',      title: 'Errors',          hint: 'Status code reference' },
]
const sections = computed(() => allSections.filter(s => !s.adminOnly || isAdmin.value))

const restTabs: { id: RestTabId; title: string }[] = [
  { id: 'auth',      title: 'Auth' },
  { id: 'account',   title: 'Account' },
  { id: 'plugins',   title: 'Plugins' },
  { id: 'skills',    title: 'Skills' },
  { id: 'files',     title: 'Skill files' },
  { id: 'agents',    title: 'Agents' },
  { id: 'validator', title: 'Validator' },
]

const activeSection = ref<SectionId>('overview')
const activeRestTab = ref<RestTabId>('auth')

function parseHash() {
  const raw = (window.location.hash || '').replace(/^#/, '')
  if (!raw) return
  const [main, sub] = raw.split('/') as [SectionId, RestTabId | undefined]
  if (sections.value.some(s => s.id === main)) {
    activeSection.value = main
    if (main === 'rest' && sub && restTabs.some(t => t.id === sub)) {
      activeRestTab.value = sub
    }
  }
}

function writeHash() {
  const hash =
    activeSection.value === 'rest'
      ? `#rest/${activeRestTab.value}`
      : `#${activeSection.value}`
  if (window.location.hash !== hash) {
    history.replaceState(null, '', hash)
  }
}

function selectSection(id: SectionId) {
  activeSection.value = id
  writeHash()
  // Scroll the content area to top on section change.
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function selectRestTab(id: RestTabId) {
  activeRestTab.value = id
  writeHash()
}

onMounted(() => {
  parseHash()
  window.addEventListener('hashchange', parseHash)
})
onBeforeUnmount(() => {
  window.removeEventListener('hashchange', parseHash)
})

watch([activeSection, activeRestTab], writeHash)

const restTabComponent = computed(() => {
  switch (activeRestTab.value) {
    case 'auth':      return RestAuthSection
    case 'account':   return RestAccountSection
    case 'plugins':   return RestPluginsSection
    case 'skills':    return RestSkillsSection
    case 'files':     return RestFilesSection
    case 'agents':    return RestAgentsSection
    case 'validator': return RestValidatorSection
  }
  return RestAuthSection
})
</script>

<template>
  <div class="dev-page">
    <header class="dev-header">
      <p class="kicker">API Reference</p>
      <h1>For Developers</h1>
      <p class="lede">
        Everything you need to talk to the marketplace from your own tools — REST,
        Git, and MCP — with the exact endpoints, parameters, and example calls.
      </p>
    </header>

    <div class="dev-layout">
      <aside class="dev-sidebar">
        <nav>
          <button
            v-for="s in sections"
            :key="s.id"
            type="button"
            class="side-link"
            :class="{ 'side-link--active': activeSection === s.id }"
            @click="selectSection(s.id)"
          >
            <span class="side-link-title">{{ s.title }}</span>
            <span class="side-link-hint">{{ s.hint }}</span>
          </button>
        </nav>
      </aside>

      <main class="dev-main">
        <OverviewSection    v-if="activeSection === 'overview'" />
        <AuthSection        v-else-if="activeSection === 'auth'" />
        <MarketplaceSection v-else-if="activeSection === 'marketplace'" />
        <GitSection         v-else-if="activeSection === 'git'" />
        <McpSection         v-else-if="activeSection === 'mcp'" />
        <CliSection         v-else-if="activeSection === 'cli'" />
        <AuditSection       v-else-if="activeSection === 'audit' && isAdmin" />
        <ErrorsSection      v-else-if="activeSection === 'errors'" />

        <template v-else-if="activeSection === 'rest'">
          <div class="rest-tabs" role="tablist">
            <button
              v-for="t in restTabs"
              :key="t.id"
              type="button"
              role="tab"
              :aria-selected="activeRestTab === t.id"
              class="rest-tab"
              :class="{ 'rest-tab--active': activeRestTab === t.id }"
              @click="selectRestTab(t.id)"
            >
              {{ t.title }}
            </button>
          </div>
          <component :is="restTabComponent" />
        </template>
      </main>
    </div>
  </div>
</template>

<style scoped>
.dev-page { min-width: 0; }

.dev-header { margin-bottom: 28px; }
.dev-header .kicker {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.26em;
  text-transform: uppercase;
  color: var(--accent);
}
.dev-header h1 { margin: 0 0 14px; }
.dev-header .lede {
  font-family: var(--serif);
  font-size: 18px;
  line-height: 1.5;
  color: var(--text-soft);
  max-width: 60ch;
  margin: 0;
}

.dev-layout {
  display: grid;
  grid-template-columns: 240px minmax(0, 1fr);
  gap: 40px;
  align-items: start;
}

.dev-sidebar {
  position: sticky;
  top: 92px;
  align-self: start;
  border-left: 1px solid var(--border-soft);
  padding-left: 14px;
}
.dev-sidebar nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.side-link {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  text-align: left;
  background: transparent;
  border: 0;
  border-left: 2px solid transparent;
  padding: 8px 10px 8px 14px;
  margin-left: -14px;
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
  color: var(--text-soft);
}
.side-link:hover {
  color: var(--text);
  background: var(--bg-2);
}
.side-link-title {
  font-family: var(--mono);
  font-size: 12px;
  letter-spacing: 0.16em;
  text-transform: uppercase;
}
.side-link-hint {
  font-size: 11.5px;
  color: var(--muted);
  letter-spacing: 0;
  text-transform: none;
}
.side-link--active {
  color: var(--text);
  border-left-color: var(--accent);
  background: var(--bg-2);
}
.side-link--active .side-link-title { color: var(--accent); }

.dev-main { min-width: 0; }

.rest-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  margin: 0 0 22px;
}
.rest-tab {
  background: transparent;
  border: 0;
  border-bottom: 2px solid transparent;
  padding: 8px 14px;
  cursor: pointer;
  color: var(--text-soft);
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.2em;
  text-transform: uppercase;
  margin-bottom: -1px;
  transition: color 0.15s ease, border-color 0.15s ease;
}
.rest-tab:hover { color: var(--text); }
.rest-tab--active {
  color: var(--accent);
  border-bottom-color: var(--accent);
}

@media (max-width: 900px) {
  .dev-layout { grid-template-columns: 1fr; gap: 18px; }
  .dev-sidebar {
    position: static;
    border-left: 0;
    border-bottom: 1px solid var(--border-soft);
    padding: 0 0 12px;
    margin-left: 0;
  }
  .dev-sidebar nav {
    flex-direction: row;
    flex-wrap: wrap;
    gap: 4px 6px;
  }
  .side-link {
    margin-left: 0;
    padding: 6px 10px;
    border-left: 0;
    border-bottom: 2px solid transparent;
  }
  .side-link-hint { display: none; }
  .side-link--active {
    border-left: 0;
    border-bottom-color: var(--accent);
    background: transparent;
  }
}
</style>

<style>
/* Shared styles for section components — un-scoped so children can use them. */
.dev-section { min-width: 0; }
.dev-subsection { min-width: 0; }

.section-head {
  margin-bottom: 18px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--border-soft);
}
.section-head h2 {
  margin: 0 0 6px;
}
.section-head .section-lede {
  margin: 0;
  color: var(--text-soft);
  font-size: 14px;
  line-height: 1.5;
  max-width: 64ch;
}

.dev-list {
  margin: 8px 0 14px;
  padding-left: 22px;
  color: var(--text-soft);
}
.dev-list li { margin: 4px 0; }
.dev-list strong { color: var(--text); }

.dev-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 12px;
}
.dev-table th, .dev-table td {
  text-align: left;
  vertical-align: top;
}

/* Endpoint blocks */
.endpoint {
  border-top: 1px solid var(--border-soft);
  padding: 22px 0 6px;
  margin: 4px 0 0;
  scroll-margin-top: 100px;
}
.endpoint:first-of-type { border-top: 0; padding-top: 6px; }

.endpoint-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}
.endpoint-method {
  display: inline-block;
  padding: 3px 9px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--bg);
  background: var(--text);
  border-radius: 0;
}
.endpoint-method.method-get    { background: var(--blue); }
.endpoint-method.method-post   { background: var(--success); color: var(--bg); }
.endpoint-method.method-put    { background: var(--accent); color: var(--text); }
.endpoint-method.method-delete { background: var(--rust); color: var(--bg); }

.endpoint-path {
  font-family: var(--mono);
  font-size: 14px;
  color: var(--text);
  background: var(--bg-2);
  border: 1px solid var(--border-soft);
  padding: 4px 10px;
  word-break: break-all;
}

.endpoint-auth {
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--muted);
  border: 1px solid var(--border);
  padding: 2px 8px;
  border-radius: 999px;
}
.endpoint-auth--public {
  color: var(--success);
  border-color: rgb(var(--success-rgb) / 0.4);
}

.endpoint-summary {
  margin: 6px 0 12px;
  color: var(--text-soft);
  font-size: 13.5px;
}

.endpoint-block { margin: 14px 0; }
.endpoint-block h4 {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--accent-2);
}
.endpoint-notes h4 { display: none; }
.endpoint-notes p { color: var(--text-soft); margin: 6px 0; }
.endpoint-notes p:first-child { margin-top: 0; }

.param-row {
  border-left: 2px solid var(--border);
  padding: 6px 0 6px 12px;
  margin: 8px 0;
}
.param-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.param-name { background: var(--bg-2); }
.param-type {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--muted);
  letter-spacing: 0.04em;
}
.param-required {
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--rust);
  border: 1px solid rgb(var(--rust-rgb) / 0.45);
  padding: 1px 6px;
  border-radius: 999px;
}
.param-desc {
  margin-top: 4px;
  color: var(--text-soft);
  font-size: 13px;
}
</style>
