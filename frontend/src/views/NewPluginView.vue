<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = useRouter()
const authStore = useAuthStore()
const name = ref('')
const description = ref('')
const authorName = ref(authStore.user?.username ?? '')
const authorEmail = ref(authStore.user?.email ?? '')
const homepage = ref('')
const license = ref(authStore.defaultLicense)
authStore.ensureMode().then(() => { license.value = authStore.defaultLicense })
const error = ref('')
const loading = ref(false)

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const p = await api.createPlugin({
      name: name.value,
      description: description.value,
      authorName: authorName.value,
      authorEmail: authorEmail.value,
      homepage: homepage.value,
      license: license.value,
    })
    router.push(`/plugins/${p.name}`)
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <h1>New plugin</h1>
  <div class="card">
    <form @submit.prevent="submit">
      <label>Name (slug, lowercase, [a-z0-9-])</label>
      <input v-model="name" required pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]" />

      <label>Description</label>
      <input v-model="description" required />

      <label>License</label>
      <input v-model="license" />

      <div class="row" style="gap: 12px">
        <div style="flex: 1">
          <label>Author name</label>
          <input v-model="authorName" />
        </div>
        <div style="flex: 1">
          <label>Author email</label>
          <input v-model="authorEmail" type="email" />
        </div>
      </div>

      <label>Homepage</label>
      <input v-model="homepage" type="url" />

      <div v-if="error" class="error">{{ error }}</div>
      <div style="margin-top: 16px">
        <button type="submit" :disabled="loading">
          {{ loading ? 'Creating…' : 'Create plugin' }}
        </button>
      </div>
    </form>
  </div>
</template>
