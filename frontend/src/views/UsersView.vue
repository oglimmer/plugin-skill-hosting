<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errMsg } from '../api'
import ErrorAlert from '../components/ErrorAlert.vue'
import { useConfirm } from '../composables/useConfirm'
import { useAuthStore } from '../stores/auth'
import type { UserSummary } from '../types'

const auth = useAuthStore()
const { confirm } = useConfirm()

const users = ref<UserSummary[]>([])
const loading = ref(true)
const error = ref('')
const busyId = ref<string | null>(null)

const pending  = computed(() => users.value.filter(u => u.status === 'pending'))
const approved = computed(() => users.value.filter(u => u.status === 'approved'))
const rejected = computed(() => users.value.filter(u => u.status === 'rejected'))

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    users.value = await api.listUsers()
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

async function approve(u: UserSummary) {
  error.value = ''
  busyId.value = u.id
  try {
    await api.approveUser(u.id)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    busyId.value = null
  }
}

async function reject(u: UserSummary) {
  const ok = await confirm({
    title: `Reject ${u.username}?`,
    message: u.status === 'approved'
      ? `${u.username} is currently approved. Rejecting will immediately revoke their access. They will not be able to log in again unless an approved user reverses this.`
      : `${u.username} will not be able to log in again unless an approved user reverses this.`,
    confirmLabel: 'Reject',
    danger: true,
  })
  if (!ok) return
  error.value = ''
  busyId.value = u.id
  try {
    await api.rejectUser(u.id)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    busyId.value = null
  }
}

const approvalFlow = computed(() => auth.userApprovalRequired)

onMounted(load)
</script>

<template>
  <h1>Users</h1>

  <p v-if="approvalFlow" class="muted intro">
    This marketplace requires existing users to approve new accounts. Pending
    requests appear below — approve to grant access, reject to keep them out.
  </p>

  <div v-if="loading" class="muted">Loading…</div>
  <ErrorAlert v-else-if="error" :message="error" />
  <template v-else>
    <section v-if="pending.length > 0" class="card section">
      <h2 class="section-title">
        Pending
        <span class="chip chip--pending">{{ pending.length }}</span>
      </h2>
      <table>
        <thead>
          <tr>
            <th>Username</th>
            <th>Email</th>
            <th>Requested</th>
            <th class="actions-col"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in pending" :key="u.id">
            <td>{{ u.username }}</td>
            <td class="muted">{{ u.email }}</td>
            <td class="muted" style="white-space: nowrap">
              <small>{{ fmt(u.createdAt) }}</small>
            </td>
            <td class="actions">
              <button
                type="button"
                class="secondary"
                :disabled="busyId === u.id"
                @click="approve(u)"
              >
                {{ busyId === u.id ? 'Working…' : 'Approve' }}
              </button>
              <button
                type="button"
                class="danger"
                :disabled="busyId === u.id"
                @click="reject(u)"
              >
                Reject
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="section">
      <h2 class="section-title">
        Approved
        <span class="chip">{{ approved.length }}</span>
      </h2>
      <div v-if="approved.length === 0" class="card">
        <p class="muted" style="margin: 0">No approved users yet.</p>
      </div>
      <table v-else class="card" style="padding: 0">
        <thead>
          <tr>
            <th style="padding-left: 20px">Username</th>
            <th>Email</th>
            <th>Joined</th>
            <th>Approved by</th>
            <th v-if="approvalFlow" class="actions-col"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in approved" :key="u.id">
            <td style="padding-left: 20px">{{ u.username }}</td>
            <td class="muted">{{ u.email }}</td>
            <td class="muted" style="white-space: nowrap">
              <small>{{ fmt(u.createdAt) }}</small>
            </td>
            <td class="muted">
              <template v-if="u.approvedByName">
                {{ u.approvedByName }}
                <small v-if="u.approvedAt"> · {{ fmt(u.approvedAt) }}</small>
              </template>
              <small v-else>—</small>
            </td>
            <td v-if="approvalFlow" class="actions">
              <button
                v-if="u.id !== auth.user?.id"
                type="button"
                class="danger"
                :disabled="busyId === u.id"
                @click="reject(u)"
              >
                Reject
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-if="rejected.length > 0" class="section">
      <details class="card">
        <summary class="muted" style="cursor: pointer">
          Rejected ({{ rejected.length }})
        </summary>
        <table style="margin-top: 12px">
          <thead>
            <tr>
              <th>Username</th>
              <th>Email</th>
              <th>First seen</th>
              <th class="actions-col"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in rejected" :key="u.id">
              <td>{{ u.username }}</td>
              <td class="muted">{{ u.email }}</td>
              <td class="muted" style="white-space: nowrap">
                <small>{{ fmt(u.createdAt) }}</small>
              </td>
              <td class="actions">
                <button
                  type="button"
                  class="secondary"
                  :disabled="busyId === u.id"
                  @click="approve(u)"
                >
                  {{ busyId === u.id ? 'Working…' : 'Approve' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </details>
    </section>
  </template>
</template>

<style scoped>
.intro {
  margin: -14px 0 24px;
  max-width: 62ch;
}
.section {
  margin-bottom: 28px;
}
.section-title {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  margin: 0 0 12px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.chip {
  display: inline-grid;
  place-items: center;
  min-width: 22px;
  padding: 0 8px;
  height: 20px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.12em;
  color: var(--muted);
  border: 1px solid var(--border);
  border-radius: 999px;
}
.chip--pending {
  color: var(--accent);
  border-color: rgba(245, 165, 36, 0.5);
}
.actions {
  text-align: right;
  white-space: nowrap;
}
.actions button + button {
  margin-left: 8px;
}
.actions-col {
  width: 1%;
}
</style>
