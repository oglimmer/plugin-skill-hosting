<script setup lang="ts">
import { onMounted } from 'vue'
import NavBar from './components/NavBar.vue'
import { useAuthStore } from './stores/auth'

const auth = useAuthStore()

onMounted(() => {
  if (auth.token && !auth.user?.apiToken) {
    auth.refreshUser().catch(() => { /* token may be invalid; let route guards handle */ })
  }
})
</script>

<template>
  <NavBar />
  <main>
    <RouterView />
  </main>
</template>
