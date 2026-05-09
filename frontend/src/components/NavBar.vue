<script setup lang="ts">
import { onMounted } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter, RouterLink } from 'vue-router'

const auth = useAuthStore()
const router = useRouter()

onMounted(() => { auth.ensureMode() })

function logout() {
  auth.logout()
  router.push('/')
}
</script>

<template>
  <nav class="top">
    <RouterLink to="/" class="brand">Plugin Marketplace</RouterLink>
    <div class="links">
      <RouterLink to="/">Plugins</RouterLink>
      <a href="/marketplace.json" target="_blank">marketplace.json</a>
      <template v-if="auth.user">
        <RouterLink to="/plugins/new" class="btn">+ New Plugin</RouterLink>
        <span class="user">{{ auth.user.username }}</span>
        <button class="secondary" @click="logout">Log out</button>
      </template>
      <template v-else>
        <RouterLink to="/login">Log in</RouterLink>
        <RouterLink v-if="auth.mode !== 'oidc'" to="/register" class="btn">Sign up</RouterLink>
      </template>
    </div>
  </nav>
</template>
