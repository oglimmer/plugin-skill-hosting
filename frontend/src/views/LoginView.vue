<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter, useRoute } from 'vue-router'

const email = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)
const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

onMounted(() => { auth.ensureMode() })

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(email.value, password.value)
    const dest = (route.query.redirect as string) || '/'
    router.push(dest)
  } catch (e: any) {
    error.value = e.message || 'login failed'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <h1>Log in</h1>
  <div class="card" style="max-width: 420px">
    <template v-if="auth.mode === 'oidc'">
      <p>Sign in with your single-sign-on provider.</p>
      <div style="margin-top: 16px">
        <button type="button" @click="auth.loginViaOIDC()">Sign in with SSO</button>
      </div>
    </template>
    <template v-else-if="auth.mode === 'password'">
      <form @submit.prevent="submit">
        <label>Email</label>
        <input v-model="email" type="email" required autocomplete="email" />
        <label>Password</label>
        <input v-model="password" type="password" required autocomplete="current-password" />
        <div v-if="error" class="error">{{ error }}</div>
        <div style="margin-top: 16px">
          <button type="submit" :disabled="loading">
            {{ loading ? 'Logging in…' : 'Log in' }}
          </button>
        </div>
        <p class="muted" style="margin-top: 16px">
          New here? <RouterLink to="/register">Create an account</RouterLink>
        </p>
      </form>
    </template>
    <template v-else>
      <p class="muted">Loading…</p>
    </template>
  </div>
</template>
