<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type Plugin } from '../api'
import { useAuthStore } from '../stores/auth'
import { RouterLink } from 'vue-router'
import { useConfirm } from '../composables/useConfirm'

const { confirm } = useConfirm()

const auth = useAuthStore()
const plugins = ref<Plugin[]>([])
const deletedPlugins = ref<Plugin[]>([])
const loading = ref(true)
const error = ref('')
const tokenError = ref('')
const regenerating = ref(false)
const copied = ref('')
const activeTab = ref<'plugins' | 'connect'>('plugins')

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

const apiToken = computed(() => auth.user?.apiToken ?? '')

const authedOrigin = computed(() => {
  if (!apiToken.value) return window.location.origin
  const u = new URL(window.location.origin)
  u.username = '_'
  u.password = apiToken.value
  // URL.toString() appends a trailing "/", strip it for clean joining.
  return u.toString().replace(/\/$/, '')
})

const marketplaceUrl = computed(() => `${authedOrigin.value}/marketplace.json`)
const marketplaceCmd = computed(() => `/plugin marketplace add ${marketplaceUrl.value}`)

const mcpUrl = computed(() => `${window.location.origin}/mcp`)
const mcpServerName = computed(() => auth.marketplaceName || 'skill-host')
const mcpAddCmd = computed(() =>
  `claude mcp add --transport http ${mcpServerName.value} ${mcpUrl.value} -H "Authorization: Bearer ${apiToken.value}"`
)
const mcpJsonConfig = computed(() => JSON.stringify({
  mcpServers: {
    [mcpServerName.value]: {
      type: 'http',
      url: mcpUrl.value,
      headers: { Authorization: `Bearer ${apiToken.value}` },
    },
  },
}, null, 2))

let initialLoad = true
async function load() {
  loading.value = true
  error.value = ''
  try {
    const [active, deleted] = await Promise.all([
      api.listPlugins(),
      auth.user ? api.listDeletedPlugins() : Promise.resolve([] as Plugin[]),
    ])
    plugins.value = active
    deletedPlugins.value = deleted
    if (initialLoad && active.length === 0) {
      activeTab.value = 'connect'
    }
    initialLoad = false
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

async function restorePlugin(name: string) {
  try {
    await api.restorePlugin(name)
    await load()
  } catch (e: any) {
    error.value = e.message
  }
}

async function regenerate() {
  const ok = await confirm({
    title: 'Regenerate API token',
    message: 'Existing marketplace links will stop working until you update them. Continue?',
    confirmLabel: 'Regenerate',
    danger: true,
  })
  if (!ok) return
  tokenError.value = ''
  regenerating.value = true
  try {
    await auth.regenerateToken()
  } catch (e: any) {
    tokenError.value = e.message
  } finally {
    regenerating.value = false
  }
}

async function copy(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => { if (copied.value === label) copied.value = '' }, 1500)
  } catch {}
}

onMounted(load)
</script>

<template>
  <h1>Plugins</h1>

  <div class="tabs" role="tablist">
    <button
      type="button"
      class="tab"
      role="tab"
      :class="{ active: activeTab === 'plugins' }"
      :aria-selected="activeTab === 'plugins'"
      @click="activeTab = 'plugins'"
    >
      Plugins
      <span class="tab-count" :class="{ 'is-empty': plugins.length === 0 }">
        {{ plugins.length }}
      </span>
    </button>
    <button
      type="button"
      class="tab"
      role="tab"
      :class="{ active: activeTab === 'connect' }"
      :aria-selected="activeTab === 'connect'"
      @click="activeTab = 'connect'"
    >
      Connect
    </button>
  </div>

  <section v-show="activeTab === 'plugins'" role="tabpanel">
    <div v-if="loading" class="muted">Loading…</div>
    <div v-else-if="error" class="error">{{ error }}</div>
    <div v-else-if="plugins.length === 0" class="card">
      <p class="muted" style="margin: 0 0 12px">No plugins yet.</p>
      <div class="row" style="gap: 12px; flex-wrap: wrap">
        <RouterLink to="/plugins/new" class="btn">Create the first one</RouterLink>
        <button type="button" class="secondary" @click="activeTab = 'connect'">
          Connect to Claude Code
        </button>
      </div>
    </div>
    <table v-else class="card" style="padding: 0">
      <thead>
        <tr>
          <th style="padding-left: 20px">Name</th>
          <th>Description</th>
          <th>Owner</th>
          <th>Version</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in plugins" :key="p.id">
          <td style="padding-left: 20px">
            <RouterLink :to="`/plugins/${p.name}`">{{ p.name }}</RouterLink>
          </td>
          <td>{{ p.description }}</td>
          <td class="muted">{{ p.ownerName }}</td>
          <td><span class="badge">{{ p.version }}</span></td>
        </tr>
      </tbody>
    </table>

    <div v-if="deletedPlugins.length > 0" class="card">
      <details>
        <summary class="muted" style="cursor: pointer">
          Deleted plugins ({{ deletedPlugins.length }})
        </summary>
        <p class="muted" style="margin: 12px 0">
          Soft-deleted; restore to put them back in the marketplace.
        </p>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Deleted</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in deletedPlugins" :key="p.id">
              <td>{{ p.name }}</td>
              <td>{{ p.description }}</td>
              <td class="muted" style="white-space: nowrap">
                <small>{{ fmt(p.deletedAt) }}</small>
              </td>
              <td style="text-align: right">
                <button class="secondary" @click="restorePlugin(p.name)">Restore</button>
              </td>
            </tr>
          </tbody>
        </table>
      </details>
    </div>
  </section>

  <section v-show="activeTab === 'connect'" role="tabpanel">
    <div class="card">
      <h2 style="margin-bottom: 4px">Add this marketplace in Claude Code</h2>
      <p class="muted" style="margin: 0 0 12px">
        The command below contains your personal API token. Keep it secret.
      </p>
      <pre style="white-space: pre-wrap; word-break: break-all">{{ marketplaceCmd }}</pre>
      <div class="row" style="gap: 8px">
        <button class="secondary" type="button" @click="copy(marketplaceCmd, 'cmd')">
          {{ copied === 'cmd' ? 'Copied' : 'Copy command' }}
        </button>
        <button class="secondary" type="button" @click="copy(marketplaceUrl, 'url')">
          {{ copied === 'url' ? 'Copied' : 'Copy URL' }}
        </button>
      </div>

      <details style="margin-top: 20px">
        <summary class="muted" style="cursor: pointer">Advanced: raw API token</summary>
        <div class="row" style="gap: 8px; align-items: stretch; margin-top: 8px">
          <input
            type="text"
            :value="apiToken"
            readonly
            style="flex: 1; font-family: ui-monospace, SFMono-Regular, Menlo, monospace"
          />
          <button class="secondary" type="button" @click="copy(apiToken, 'token')">
            {{ copied === 'token' ? 'Copied' : 'Copy' }}
          </button>
          <button class="danger" type="button" :disabled="regenerating" @click="regenerate">
            {{ regenerating ? 'Regenerating…' : 'Regenerate' }}
          </button>
        </div>
        <div v-if="tokenError" class="error" style="margin-top: 8px">{{ tokenError }}</div>
      </details>
    </div>

    <div class="card">
      <h2 style="margin-bottom: 4px">Use this marketplace as an MCP server</h2>
      <p class="muted" style="margin: 0 0 12px">
        Lets Claude (or any MCP-aware client) read plugins and create / modify skills directly.
        Tools: <code>list_plugins</code>, <code>get_plugin</code>, <code>get_skill</code>,
        <code>create_skill</code>, <code>update_skill</code>, <code>list_skill_files</code>,
        <code>get_skill_file</code>, <code>upsert_skill_file</code>.
        Plugins are read-only over MCP; nothing can be deleted.
      </p>

      <p style="margin: 0 0 4px"><strong>Claude Code (one-line install):</strong></p>
      <pre style="white-space: pre-wrap; word-break: break-all">{{ mcpAddCmd }}</pre>
      <div class="row" style="gap: 8px">
        <button class="secondary" type="button" @click="copy(mcpAddCmd, 'mcpCmd')">
          {{ copied === 'mcpCmd' ? 'Copied' : 'Copy command' }}
        </button>
        <button class="secondary" type="button" @click="copy(mcpUrl, 'mcpUrl')">
          {{ copied === 'mcpUrl' ? 'Copied' : 'Copy URL' }}
        </button>
      </div>

      <details style="margin-top: 20px">
        <summary class="muted" style="cursor: pointer">JSON config (Claude Desktop and other MCP clients)</summary>
        <p class="muted" style="margin: 8px 0">
          Paste under <code>mcpServers</code> in your client's MCP config.
        </p>
        <pre style="white-space: pre-wrap; word-break: break-all">{{ mcpJsonConfig }}</pre>
        <button class="secondary" type="button" @click="copy(mcpJsonConfig, 'mcpJson')">
          {{ copied === 'mcpJson' ? 'Copied' : 'Copy JSON' }}
        </button>
      </details>
    </div>
  </section>
</template>

<style scoped>
.tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  margin: 0 0 28px;
}
.tab {
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  padding: 12px 22px;
  margin-bottom: -1px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 10px;
  transition: color 0.2s ease, border-color 0.2s ease;
}
.tab::before { display: none; }
.tab:hover { color: var(--text); transform: none; }
.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.tab-count {
  display: inline-grid;
  place-items: center;
  min-width: 20px;
  padding: 0 6px;
  height: 18px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.12em;
  color: var(--muted);
  border: 1px solid var(--border);
  border-radius: 999px;
  background: transparent;
  transition: opacity 0.2s ease, color 0.2s ease, border-color 0.2s ease;
}
.tab-count.is-empty {
  /* Reserve the space so the adjacent tab never shifts; just fade the chip. */
  opacity: 0;
}
.tab.active .tab-count {
  color: var(--accent-2);
  border-color: rgba(245, 165, 36, 0.45);
}
</style>
