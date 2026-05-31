<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useAuthStore } from '../stores/auth'
import { useRouter } from 'vue-router'
import type { User } from '../types'

const auth = useAuthStore()
const router = useRouter()

// Friendly copy for each backend reason code (see oidcErr* in oidc.go). retry
// is true for transient/technical failures worth re-attempting; false for
// policy/identity outcomes the user can't fix by retrying.
type Reason = { title: string; message: string; retry: boolean }
const REASONS: Record<string, Reason> = {
  provider_error: {
    title: 'Sign-in didn’t complete',
    message:
      'We couldn’t finish signing you in with your identity provider. This is usually temporary — please try again.',
    retry: true,
  },
  domain_not_allowed: {
    title: 'Your domain isn’t allowed',
    message:
      'Sign-in to this marketplace is restricted to approved email domains, and yours isn’t on the list. If you think this is a mistake, contact an administrator.',
    retry: false,
  },
  account_rejected: {
    title: 'Access was declined',
    message:
      'An administrator has declined access for this account. Reach out to your administrator if you need access.',
    retry: false,
  },
  account_deleted: {
    title: 'Account no longer active',
    message:
      'This account has been removed and can no longer sign in. Contact an administrator if you need access restored.',
    retry: false,
  },
  email_conflict: {
    title: 'We couldn’t link your account',
    message:
      'An account with your email address already exists, but your identity provider didn’t confirm that you own that email — so we didn’t link them automatically, to keep the account secure. An administrator can resolve this for you.',
    retry: false,
  },
  account_error: {
    title: 'Sign-in failed',
    message:
      'Something went wrong while setting up your account. Please try again — if it keeps happening, contact an administrator.',
    retry: true,
  },
}

const GENERIC: Reason = {
  title: 'Sign-in failed',
  message: 'We couldn’t sign you in. Please try again.',
  retry: true,
}

const failure = ref<Reason | null>(null)

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

  const errParam = params.get('error')
  if (errParam) {
    failure.value = REASONS[errParam] ?? GENERIC
    return
  }
  const token = params.get('token')
  const userParam = params.get('user')
  if (!token || !userParam) {
    failure.value = GENERIC
    return
  }
  try {
    auth.setSession(token, decodeUser(userParam))
  } catch {
    failure.value = GENERIC
    return
  }
  // Wipe the hash from the URL before navigating away. Route guards take it from
  // here — an approved user lands on the catalogue, a pending one on /pending.
  history.replaceState(null, '', window.location.pathname)
  router.replace('/')
})
</script>

<template>
  <div class="cb">
    <template v-if="failure">
      <h1 class="cb__title">{{ failure.title }}</h1>
      <p class="cb__msg">{{ failure.message }}</p>
      <div class="cb__actions">
        <RouterLink v-if="failure.retry" to="/login" class="cb__btn cb__btn--primary">
          Try again
        </RouterLink>
        <RouterLink to="/login" class="cb__link">Back to sign in</RouterLink>
      </div>
    </template>
    <template v-else>
      <p class="cb__pending">Signing you in…</p>
    </template>
  </div>
</template>

<style scoped>
.cb {
  max-width: 460px;
  margin: 12vh auto 0;
  padding: 2rem;
  text-align: center;
}
.cb__title {
  font-size: 1.25rem;
  margin: 0 0 0.75rem;
}
.cb__msg {
  color: var(--text-soft, #555);
  line-height: 1.5;
  margin: 0 0 1.5rem;
}
.cb__actions {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.75rem;
}
.cb__btn--primary {
  display: inline-block;
  background: var(--accent, #6366f1);
  color: #fff;
  padding: 0.55rem 1.25rem;
  border-radius: 6px;
  text-decoration: none;
}
.cb__link {
  color: var(--muted, #777);
  font-size: 0.9rem;
}
.cb__pending {
  color: var(--muted, #777);
  text-align: center;
  margin-top: 12vh;
}
</style>
