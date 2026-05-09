<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { api, type Plugin } from '../api'
import { useAuthStore } from '../stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const plugin = ref<Plugin | null>(null)
const loading = ref(true)
const error = ref('')
const copied = ref('')

const isOwner = computed(() =>
  !!(plugin.value && auth.user && plugin.value.ownerId === auth.user.id),
)

const installCmd = computed(() => {
  if (!plugin.value) return ''
  const market = auth.marketplaceName
  return market
    ? `/plugin install ${plugin.value.name}@${market}`
    : `/plugin install ${plugin.value.name}`
})

async function copy(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => { if (copied.value === label) copied.value = '' }, 1500)
  } catch {}
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    plugin.value = await api.getPlugin(route.params.name as string)
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

async function deleteSkill(name: string) {
  if (!plugin.value) return
  if (!confirm(`Delete skill "${name}"?`)) return
  try {
    await api.deleteSkill(plugin.value.name, name)
    await load()
  } catch (e: any) {
    error.value = e.message
  }
}

async function deletePlugin() {
  if (!plugin.value) return
  if (!confirm(`Delete plugin "${plugin.value.name}" and all its skills?`)) return
  try {
    await api.deletePlugin(plugin.value.name)
    router.push('/')
  } catch (e: any) {
    error.value = e.message
  }
}

onMounted(() => {
  load()
  auth.ensureMode()
})
</script>

<template>
  <div v-if="loading" class="muted">Loading…</div>
  <div v-else-if="error" class="error">{{ error }}</div>
  <div v-else-if="plugin">
    <div class="row" style="margin-bottom: 16px">
      <h1 style="margin: 0">{{ plugin.name }}</h1>
      <span class="badge">{{ plugin.version }}</span>
      <div class="spacer" />
      <button v-if="isOwner" class="danger" @click="deletePlugin">Delete plugin</button>
    </div>
    <p class="muted" style="margin-top: 0">{{ plugin.description }}</p>

    <div class="card">
      <h2 style="margin-bottom: 4px">Install this plugin in Claude Code</h2>
      <p class="muted" style="margin: 0 0 12px">
        Make sure you've added the marketplace first (see the Plugins page).
      </p>
      <pre style="white-space: pre-wrap; word-break: break-all">{{ installCmd }}</pre>
      <div class="row" style="gap: 8px">
        <button class="secondary" type="button" @click="copy(installCmd, 'cmd')">
          {{ copied === 'cmd' ? 'Copied' : 'Copy command' }}
        </button>
      </div>
    </div>

    <div class="card">
      <div class="row">
        <h2 style="margin: 0">Skills</h2>
        <div class="spacer" />
        <RouterLink
          v-if="isOwner"
          :to="`/plugins/${plugin.name}/skills/new`"
          class="btn"
        >+ New skill</RouterLink>
      </div>
      <p v-if="!plugin.skills || plugin.skills.length === 0" class="muted">
        No skills yet.
      </p>
      <table v-else>
        <thead>
          <tr>
            <th>Name</th>
            <th>Description</th>
            <th v-if="isOwner"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in plugin.skills" :key="s.id">
            <td>
              <RouterLink
                v-if="isOwner"
                :to="`/plugins/${plugin.name}/skills/${s.name}/edit`"
              >{{ s.name }}</RouterLink>
              <span v-else>{{ s.name }}</span>
            </td>
            <td>{{ s.description }}</td>
            <td v-if="isOwner" style="text-align: right">
              <button class="secondary" @click="deleteSkill(s.name)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h2>Metadata</h2>
      <table>
        <tbody>
          <tr><th>Owner</th><td>{{ plugin.ownerName }}</td></tr>
          <tr v-if="plugin.authorName"><th>Author</th><td>{{ plugin.authorName }}</td></tr>
          <tr v-if="plugin.authorEmail"><th>Email</th><td>{{ plugin.authorEmail }}</td></tr>
          <tr v-if="plugin.homepage"><th>Homepage</th><td><a :href="plugin.homepage" target="_blank">{{ plugin.homepage }}</a></td></tr>
          <tr v-if="plugin.license"><th>License</th><td>{{ plugin.license }}</td></tr>
          <tr><th>Updated</th><td>{{ new Date(plugin.updatedAt).toLocaleString() }}</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
