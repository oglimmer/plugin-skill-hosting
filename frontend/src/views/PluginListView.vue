<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type Plugin } from '../api'
import { useAuthStore } from '../stores/auth'
import { RouterLink } from 'vue-router'

const auth = useAuthStore()
const plugins = ref<Plugin[]>([])
const loading = ref(true)
const error = ref('')

const marketplaceUrl = `${window.location.origin}/marketplace.json`

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

onMounted(load)
</script>

<template>
  <h1>Plugins</h1>

  <div class="card">
    <h2 style="margin-bottom: 4px">For Claude Code users</h2>
    <p class="muted" style="margin: 0 0 12px">
      Add this marketplace from inside Claude Code:
    </p>
    <pre>/plugin marketplace add {{ marketplaceUrl }}</pre>
  </div>

  <div v-if="loading" class="muted">Loading…</div>
  <div v-else-if="error" class="error">{{ error }}</div>
  <div v-else-if="plugins.length === 0" class="card">
    <p class="muted">No plugins yet.</p>
    <RouterLink v-if="auth.user" to="/plugins/new" class="btn">Create the first one</RouterLink>
    <RouterLink v-else to="/register" class="btn">Sign up to create one</RouterLink>
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
