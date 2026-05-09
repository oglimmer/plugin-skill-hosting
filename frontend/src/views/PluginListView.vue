<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type Plugin } from '../api'
import { useAuthStore } from '../stores/auth'
import { RouterLink } from 'vue-router'

const auth = useAuthStore()
const plugins = ref<Plugin[]>([])
const loading = ref(true)
const error = ref('')
const tokenError = ref('')
const regenerating = ref(false)
const showToken = ref(false)
const copied = ref('')

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

async function load() {
  loading.value = true
  error.value = ''
  try {
    plugins.value = await api.listPlugins()
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

async function regenerate() {
  if (!confirm('Regenerate your API token? Existing marketplace links will stop working until you update them.')) return
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

  <div class="card">
    <h2 style="margin-bottom: 4px">Your marketplace access</h2>
    <p class="muted" style="margin: 0 0 12px">
      Anyone with this token can fetch the marketplace and clone plugin repos as you.
      Keep it secret. Regenerate if it leaks.
    </p>

    <label style="display:block; margin-top: 8px">API token</label>
    <div class="row" style="gap: 8px; align-items: stretch">
      <input
        :type="showToken ? 'text' : 'password'"
        :value="apiToken"
        readonly
        style="flex: 1; font-family: ui-monospace, SFMono-Regular, Menlo, monospace"
      />
      <button class="secondary" type="button" @click="showToken = !showToken">
        {{ showToken ? 'Hide' : 'Show' }}
      </button>
      <button class="secondary" type="button" @click="copy(apiToken, 'token')">
        {{ copied === 'token' ? 'Copied' : 'Copy' }}
      </button>
      <button class="danger" type="button" :disabled="regenerating" @click="regenerate">
        {{ regenerating ? 'Regenerating…' : 'Regenerate' }}
      </button>
    </div>
    <div v-if="tokenError" class="error" style="margin-top: 8px">{{ tokenError }}</div>

    <h3 style="margin-top: 20px; margin-bottom: 4px">Add this marketplace in Claude Code</h3>
    <pre style="white-space: pre-wrap; word-break: break-all">{{ marketplaceCmd }}</pre>
    <div class="row" style="gap: 8px">
      <button class="secondary" type="button" @click="copy(marketplaceCmd, 'cmd')">
        {{ copied === 'cmd' ? 'Copied' : 'Copy command' }}
      </button>
      <button class="secondary" type="button" @click="copy(marketplaceUrl, 'url')">
        {{ copied === 'url' ? 'Copied' : 'Copy URL' }}
      </button>
    </div>
  </div>

  <div v-if="loading" class="muted">Loading…</div>
  <div v-else-if="error" class="error">{{ error }}</div>
  <div v-else-if="plugins.length === 0" class="card">
    <p class="muted">No plugins yet.</p>
    <RouterLink to="/plugins/new" class="btn">Create the first one</RouterLink>
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
</template>
