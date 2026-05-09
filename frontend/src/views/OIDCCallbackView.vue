<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'
import type { User } from '../api'

const error = ref('')
const auth = useAuthStore()
const router = useRouter()

function decodeUser(b64: string): User {
  const norm = b64.replace(/-/g, '+').replace(/_/g, '/')
  const pad = norm.length % 4 === 0 ? norm : norm + '='.repeat(4 - (norm.length % 4))
  return JSON.parse(atob(pad))
}

onMounted(() => {
  const hash = window.location.hash.startsWith('#')
    ? window.location.hash.slice(1)
    : window.location.hash
  const params = new URLSearchParams(hash)

  const errMsg = params.get('error')
  if (errMsg) {
    error.value = errMsg
    return
  }
  const token = params.get('token')
  const userParam = params.get('user')
  if (!token || !userParam) {
    error.value = 'missing token or user'
    return
  }
  try {
    auth.setSession(token, decodeUser(userParam))
  } catch (e: any) {
    error.value = 'failed to parse session: ' + (e?.message ?? e)
    return
  }
  // Wipe the hash from the URL before navigating away.
  history.replaceState(null, '', window.location.pathname)
  router.replace('/')
})
</script>

<template>
  <div class="card" style="max-width: 420px">
    <template v-if="error">
      <h1>Sign-in failed</h1>
      <div class="error">{{ error }}</div>
      <p style="margin-top: 16px">
        <RouterLink to="/login">Try again</RouterLink>
      </p>
    </template>
    <template v-else>
      <p class="muted">Signing you in…</p>
    </template>
  </div>
</template>
