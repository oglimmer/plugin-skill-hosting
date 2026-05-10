<script setup lang="ts">
import { onMounted } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter, RouterLink } from 'vue-router'

const auth = useAuthStore()
const router = useRouter()

onMounted(() => { auth.ensureMode() })

function logout() {
  auth.logout()
  router.push('/login')
}
</script>

<template>
  <nav class="top">
    <RouterLink to="/" class="brand" aria-label="Plugin marketplace home">
      <svg viewBox="0 0 32 32" width="20" height="20" aria-hidden="true">
        <path d="M4 8 L16 2 L28 8 L28 24 L16 30 L4 24 Z"
              fill="none" stroke="currentColor" stroke-width="1.4" />
        <path d="M16 2 L16 30 M4 8 L28 24 M28 8 L4 24"
              stroke="currentColor" stroke-width="0.6" opacity="0.45" />
      </svg>
      <span>plugin&nbsp;/&nbsp;market</span>
    </RouterLink>
    <div class="links">
      <template v-if="auth.user">
        <RouterLink to="/">Plugins</RouterLink>
        <RouterLink to="/developers">Developers</RouterLink>
        <RouterLink to="/plugins/new" class="btn">+ New plugin</RouterLink>
        <span class="user">{{ auth.user.username }}</span>
        <button class="secondary" @click="logout">Log out</button>
      </template>
      <template v-else>
        <RouterLink to="/developers">Developers</RouterLink>
        <RouterLink to="/login">Log in</RouterLink>
        <RouterLink v-if="auth.mode !== 'oidc'" to="/register" class="btn">Sign up</RouterLink>
      </template>
    </div>
  </nav>
</template>
