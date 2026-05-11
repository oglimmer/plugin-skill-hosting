<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()

const status = computed(() => auth.user?.status ?? 'pending')
const isRejected = computed(() => status.value === 'rejected')

let poll: number | null = null

async function refresh() {
  try {
    await auth.refreshUser()
    if (auth.user?.status === 'approved') {
      router.push('/')
    }
  } catch {
    // Token likely invalidated; route guard / login flow will clean up.
  }
}

function logout() {
  if (auth.doLogout()) return // full-page redirect already in flight
  router.push('/login')
}

onMounted(() => {
  refresh()
  // Cheap heartbeat: every 5s a pending viewer learns about approval without
  // a manual refresh. Rejected users get the same channel for symmetry but
  // there's nothing for them to recover here.
  poll = window.setInterval(refresh, 5000)
})

onBeforeUnmount(() => {
  if (poll !== null) window.clearInterval(poll)
})
</script>

<template>
  <div class="pending-page">
    <div class="card pending-card">
      <p class="kicker">{{ isRejected ? 'Access denied' : 'Awaiting approval' }}</p>
      <h1 v-if="isRejected">Your account was rejected.</h1>
      <h1 v-else>Your account is awaiting approval.</h1>

      <p class="lede" v-if="isRejected">
        An existing user has declined access for this account. If you think
        this is a mistake, please contact a marketplace member directly.
      </p>
      <p class="lede" v-else>
        Hi <strong>{{ auth.user?.username }}</strong> — your sign-in worked,
        but new accounts on this marketplace need to be confirmed by an
        existing member before they can browse plugins or publish skills.
        This page will refresh automatically once you've been approved.
      </p>

      <div class="row">
        <button type="button" class="secondary" @click="refresh">
          Check again
        </button>
        <button type="button" class="danger" @click="logout">
          Log out
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pending-page {
  min-height: calc(100vh - 200px);
  display: grid;
  place-items: center;
  padding: 60px 24px;
}
.pending-card {
  max-width: 560px;
  text-align: left;
}
.kicker {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.26em;
  text-transform: uppercase;
  color: var(--accent);
}
.pending-card h1 { margin: 0 0 18px; }
.lede {
  font-family: var(--serif);
  font-size: 17px;
  line-height: 1.55;
  color: var(--text-soft);
  margin: 0 0 24px;
}
.row {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
</style>
