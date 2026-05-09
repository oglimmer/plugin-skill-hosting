<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'

const email = ref('')
const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)
const auth = useAuthStore()
const router = useRouter()

const passwordMode = computed(() => auth.mode === 'password')

onMounted(() => { auth.ensureMode() })

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.register(email.value, username.value, password.value)
    router.push('/')
  } catch (e: any) {
    error.value = e.message || 'registration failed'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <h1>Create an account</h1>
  <div class="card" style="max-width: 420px">
    <template v-if="passwordMode">
      <form @submit.prevent="submit">
        <label>Email</label>
        <input v-model="email" type="email" required autocomplete="email" />
        <label>Username</label>
        <input v-model="username" required autocomplete="username" pattern="[a-zA-Z0-9_-]{3,32}" />
        <label>Password (min 8 chars)</label>
        <input v-model="password" type="password" required minlength="8" autocomplete="new-password" />
        <div v-if="error" class="error">{{ error }}</div>
        <div style="margin-top: 16px">
          <button type="submit" :disabled="loading">
            {{ loading ? 'Creating…' : 'Sign up' }}
          </button>
        </div>
        <p class="muted" style="margin-top: 16px">
          Already have an account? <RouterLink to="/login">Log in</RouterLink>
        </p>
      </form>
    </template>
    <template v-else-if="auth.mode === 'oidc'">
      <p>This server uses single sign-on. Accounts are created automatically the first time you sign in.</p>
      <div style="margin-top: 16px">
        <button type="button" @click="auth.loginViaOIDC()">Sign in with SSO</button>
      </div>
    </template>
    <template v-else>
      <p class="muted">Loading…</p>
    </template>
  </div>
</template>
