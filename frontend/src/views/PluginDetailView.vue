<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { storeToRefs } from 'pinia'
import { api, errMsg } from '../api'
import type { Skill } from '../types'
import ErrorAlert from '../components/ErrorAlert.vue'
import { useAuthStore } from '../stores/auth'
import { usePluginStore } from '../stores/plugins'
import { useConfirm } from '../composables/useConfirm'

const { confirm } = useConfirm()

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const pluginStore = usePluginStore()
const { current: plugin } = storeToRefs(pluginStore)
const deletedSkills = ref<Skill[]>([])
const loading = ref(true)
const error = ref('')
const copied = ref('')

const isOwner = computed(() =>
  !!(plugin.value && auth.user && plugin.value.ownerId === auth.user.id),
)
const isAuthed = computed(() => !!auth.user)

const installCmd = computed(() => {
  if (!plugin.value) return ''
  const market = auth.marketplaceName
  return market
    ? `/plugin install ${plugin.value.name}@${market}`
    : `/plugin install ${plugin.value.name}`
})

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

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
    const name = route.params.name as string
    await pluginStore.loadPlugin(name)
    deletedSkills.value = await api.listDeletedSkills(name)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

async function deleteSkill(name: string) {
  if (!plugin.value) return
  const ok = await confirm({
    title: 'Delete skill',
    message: `Delete skill "${name}"? You can restore it later from the Deleted skills section.`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteSkill(plugin.value.name, name)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

async function restoreSkill(name: string) {
  if (!plugin.value) return
  try {
    await api.restoreSkill(plugin.value.name, name)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

async function deletePlugin() {
  if (!plugin.value) return
  const ok = await confirm({
    title: 'Delete plugin',
    message: `Delete plugin "${plugin.value.name}"? It will be hidden from the marketplace and \`git clone\` will stop serving it. You can restore it later from the Plugins page.`,
    confirmLabel: 'Delete plugin',
    danger: true,
  })
  if (!ok) return
  try {
    await pluginStore.deletePlugin(plugin.value.name)
    router.push('/')
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

onMounted(() => {
  load()
  auth.ensureMode()
})
</script>

<template>
  <div v-if="loading" class="muted">Loading…</div>
  <ErrorAlert v-else-if="error" :message="error" />
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
          v-if="isAuthed"
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
            <th>Created</th>
            <th>Last edited</th>
            <th v-if="isAuthed"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in plugin.skills" :key="s.id">
            <td>
              <RouterLink
                v-if="isAuthed"
                :to="`/plugins/${plugin.name}/skills/${s.name}/edit`"
              >{{ s.name }}</RouterLink>
              <span v-else>{{ s.name }}</span>
            </td>
            <td>{{ s.description }}</td>
            <td class="muted" style="white-space: nowrap">
              <span v-if="s.createdByName">{{ s.createdByName }}</span>
              <span v-else>—</span>
              <br />
              <small>{{ fmt(s.createdAt) }}</small>
            </td>
            <td class="muted" style="white-space: nowrap">
              <span v-if="s.updatedByName">{{ s.updatedByName }}</span>
              <span v-else>—</span>
              <br />
              <small>{{ fmt(s.updatedAt) }}</small>
            </td>
            <td v-if="isAuthed" style="text-align: right">
              <button class="secondary" @click="deleteSkill(s.name)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="deletedSkills.length > 0" class="card">
      <details>
        <summary class="muted" style="cursor: pointer">
          Deleted skills ({{ deletedSkills.length }})
        </summary>
        <p class="muted" style="margin: 12px 0">
          Soft-deleted; restore to bring them back into the plugin.
        </p>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Deleted</th>
              <th v-if="isAuthed"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in deletedSkills" :key="s.id">
              <td>{{ s.name }}</td>
              <td>{{ s.description }}</td>
              <td class="muted" style="white-space: nowrap">
                <span v-if="s.deletedByName">{{ s.deletedByName }}</span>
                <span v-else>—</span>
                <br />
                <small>{{ fmt(s.deletedAt) }}</small>
              </td>
              <td v-if="isAuthed" style="text-align: right">
                <button class="secondary" @click="restoreSkill(s.name)">Restore</button>
              </td>
            </tr>
          </tbody>
        </table>
      </details>
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
          <tr><th>Updated</th><td>{{ fmt(plugin.updatedAt) }}</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
