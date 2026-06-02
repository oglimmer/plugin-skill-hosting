<script setup lang="ts">
import { onMounted, ref, computed, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { storeToRefs } from 'pinia'
import { api, errMsg, errStatus } from '../api'
import type { Skill } from '../types'
import ErrorAlert from '../components/ErrorAlert.vue'
import ErrorView from './ErrorView.vue'
import { useAuthStore } from '../stores/auth'
import { usePluginStore } from '../stores/plugins'
import { useConfirm } from '../composables/useConfirm'

const { confirm } = useConfirm()

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const pluginStore = usePluginStore()
const { current: plugin } = storeToRefs(pluginStore)
const deletedSkills = ref<Skill[]>([])
const loading = ref(true)
const error = ref('')
// When the *initial load* fails (missing plugin, server error) we take over
// the whole view with a full-page ErrorView. In-page action errors (failed
// edit/delete) keep `loadErrorCode` null and surface as an inline alert so the
// loaded plugin stays on screen.
const loadErrorCode = ref<number | null>(null)
const copied = ref('')
const activeTab = ref<'skills' | 'connect'>('skills')
// In enterprise mode the manual install command is tucked behind expert mode,
// since plugins are normally enabled fleet-wide via managed settings.
const expertMode = ref(false)

const editing = ref(false)
const saving = ref(false)
const editError = ref('')
const editForm = ref({
  description: '',
  authorName: '',
  authorEmail: '',
  homepage: '',
  license: '',
})

const isOwner = computed(() =>
  !!(plugin.value && auth.user && plugin.value.ownerId === auth.user.id),
)
const isAuthed = computed(() => !!auth.user)

const installCmd = computed(() => {
  if (!plugin.value) return ''
  const market = auth.marketplaceName
  return market
    ? `/plugin install ${plugin.value.name}@${market}`
    : `/plugin install ${plugin.value.name}`
})

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

async function copy(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => { if (copied.value === label) copied.value = '' }, 1500)
  } catch {}
}

async function load() {
  loading.value = true
  error.value = ''
  loadErrorCode.value = null
  try {
    const name = route.params.name as string
    await pluginStore.loadPlugin(name)
    deletedSkills.value = await api.listDeletedSkills(name)
  } catch (e: unknown) {
    error.value = errMsg(e)
    loadErrorCode.value = errStatus(e) ?? 500
  } finally {
    loading.value = false
  }
}

async function restoreSkill(name: string) {
  if (!plugin.value) return
  try {
    await api.restoreSkill(plugin.value.name, name)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

function startEdit() {
  if (!plugin.value) return
  editForm.value = {
    description: plugin.value.description ?? '',
    authorName: plugin.value.authorName ?? '',
    authorEmail: plugin.value.authorEmail ?? '',
    homepage: plugin.value.homepage ?? '',
    license: plugin.value.license ?? '',
  }
  editError.value = ''
  editing.value = true
  // Switch to the tab that holds the form so it isn't hidden.
  activeTab.value = 'connect'
}

function cancelEdit() {
  editing.value = false
  editError.value = ''
}

async function saveEdit() {
  if (!plugin.value) return
  editError.value = ''
  saving.value = true
  try {
    await pluginStore.updatePlugin(plugin.value.name, { ...editForm.value })
    editing.value = false
  } catch (e: unknown) {
    editError.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

async function deletePlugin() {
  if (!plugin.value) return
  const ok = await confirm({
    title: 'Delete plugin',
    message: `Delete plugin "${plugin.value.name}"? It will be hidden from the marketplace and \`git clone\` will stop serving it. You can restore it later from the Plugins page.`,
    confirmLabel: 'Delete plugin',
    danger: true,
  })
  if (!ok) return
  try {
    await pluginStore.deletePlugin(plugin.value.name)
    router.push('/')
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

onMounted(() => {
  load()
  auth.ensureMode()
})
// Vue Router reuses this component when only :name changes (e.g. navigating
// from /plugins/a to /plugins/b), so onMounted won't fire again — reload
// explicitly when the route param changes to avoid showing the prior plugin.
watch(() => route.params.name, load)
</script>

<template>
  <p v-if="loading" class="pd-loading">loading…</p>
  <ErrorView
    v-else-if="loadErrorCode !== null"
    :code="loadErrorCode"
    :title="loadErrorCode === 404 ? 'Plugin not found' : undefined"
    :details="error"
  />

  <div v-else-if="plugin" class="pd">
    <ErrorAlert v-if="error" :message="error" />
    <!-- Identity bar -->
    <header class="pd-bar">
      <div class="pd-bar__id">
        <span class="pd-bar__kind">PLUGIN</span>
        <span class="pd-bar__divider"></span>
        <code class="pd-bar__path">{{ plugin.name }}</code>
        <span class="pd-bar__ver">v{{ plugin.version }}</span>
      </div>
      <div class="pd-bar__actions">
        <button
          v-if="isOwner && !editing"
          type="button"
          class="pd-btn"
          @click="startEdit"
        >edit metadata</button>
        <button
          v-if="isOwner"
          type="button"
          class="pd-btn pd-btn--danger"
          @click="deletePlugin"
        >delete plugin</button>
      </div>
    </header>

    <p v-if="plugin.description" class="pd-desc">{{ plugin.description }}</p>

    <!-- Tabs -->
    <nav class="pd-tabs" role="tablist">
      <button
        type="button"
        class="pd-tab"
        role="tab"
        :class="{ 'pd-tab--active': activeTab === 'skills' }"
        :aria-selected="activeTab === 'skills'"
        @click="activeTab = 'skills'"
      >
        skills
        <span class="pd-tab__count">[{{ plugin.skills?.length ?? 0 }}]</span>
      </button>
      <button
        type="button"
        class="pd-tab"
        role="tab"
        :class="{ 'pd-tab--active': activeTab === 'connect' }"
        :aria-selected="activeTab === 'connect'"
        @click="activeTab = 'connect'"
      >connect &amp; meta</button>
    </nav>

    <!-- SKILLS tab -->
    <section v-show="activeTab === 'skills'" role="tabpanel">
      <div class="pd-toolbar">
        <span class="pd-toolbar__count" v-if="plugin.skills?.length">
          {{ plugin.skills.length }} skill{{ plugin.skills.length === 1 ? '' : 's' }}
        </span>
        <span class="spacer"></span>
        <RouterLink
          v-if="isAuthed"
          :to="`/plugins/${plugin.name}/skills/new`"
          class="pd-btn pd-btn--primary"
        >+ new skill</RouterLink>
      </div>

      <div v-if="!plugin.skills || plugin.skills.length === 0" class="pd-empty">
        <p class="pd-empty__line">
          <span class="pd-empty__prompt">$</span>
          no skills yet
        </p>
        <p class="pd-empty__hint" v-if="isAuthed">
          add one above to start populating this plugin.
        </p>
      </div>

      <table v-else class="pd-table">
        <thead>
          <tr>
            <th>name</th>
            <th>description</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in plugin.skills" :key="s.id">
            <td class="pd-table__name">
              <RouterLink
                v-if="isAuthed"
                :to="`/plugins/${plugin.name}/skills/${s.name}/edit`"
              >{{ s.name }}</RouterLink>
              <span v-else>{{ s.name }}</span>
            </td>
            <td class="pd-table__desc">{{ s.description }}</td>
          </tr>
        </tbody>
      </table>

      <details v-if="deletedSkills.length > 0" class="pd-disclosure">
        <summary class="pd-disclosure__head">
          <span class="pd-disclosure__toggle" aria-hidden="true"></span>
          <span class="pd-disclosure__title">deleted skills</span>
          <span class="pd-disclosure__count">{{ deletedSkills.length }}</span>
          <span class="spacer"></span>
          <span class="pd-disclosure__hint" aria-hidden="true">
            <span class="pd-disclosure__hint-open">expand</span>
            <span class="pd-disclosure__hint-close">collapse</span>
            <span class="pd-disclosure__chev">▸</span>
          </span>
        </summary>
        <p class="pd-disclosure__note">
          soft-deleted · restore to bring them back into the plugin.
        </p>
        <table class="pd-table pd-table--nested">
          <thead>
            <tr>
              <th>name</th>
              <th>description</th>
              <th>deleted</th>
              <th v-if="isAuthed"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in deletedSkills" :key="s.id">
              <td>{{ s.name }}</td>
              <td class="pd-table__desc">{{ s.description }}</td>
              <td class="pd-table__when">
                <span>{{ s.deletedByName || '—' }}</span>
                <span class="pd-table__when-time">· {{ fmt(s.deletedAt) }}</span>
              </td>
              <td v-if="isAuthed" class="pd-table__act">
                <button type="button" class="pd-btn" @click="restoreSkill(s.name)">restore</button>
              </td>
            </tr>
          </tbody>
        </table>
      </details>
    </section>

    <!-- CONNECT & META tab -->
    <section v-show="activeTab === 'connect'" role="tabpanel">
      <!-- ENTERPRISE: plugins ship via managed settings; manual install is expert-only -->
      <div v-if="auth.enterpriseMode && !expertMode" class="pd-block">
        <header class="pd-block__head">
          <span class="pd-block__title">install</span>
        </header>
        <p class="pd-block__body">
          this plugin is rolled out to your team automatically through claude code
          managed settings — there's nothing to install by hand. see the
          <RouterLink to="/">Plugins page</RouterLink> for the team setup snippets.
        </p>
        <div class="pd-code-actions">
          <button type="button" class="pd-btn" @click="expertMode = true">
            expert mode — show manual install command →
          </button>
        </div>
      </div>

      <div v-else class="pd-block">
        <header class="pd-block__head">
          <span class="pd-block__title">install command</span>
        </header>
        <p class="pd-block__body">
          make sure you've added the marketplace first (see the Plugins page).
        </p>
        <div class="pd-code">
          <pre>{{ installCmd }}</pre>
        </div>
        <div class="pd-code-actions">
          <button type="button" class="pd-btn" @click="copy(installCmd, 'cmd')">
            {{ copied === 'cmd' ? '✓ copied' : 'copy command' }}
          </button>
          <button
            v-if="auth.enterpriseMode"
            type="button"
            class="pd-btn"
            @click="expertMode = false"
          >← team setup</button>
        </div>
      </div>

      <div class="pd-block">
        <header class="pd-block__head">
          <span class="pd-block__title">metadata</span>
          <span v-if="editing" class="pd-block__editing">· editing</span>
        </header>

        <dl v-if="!editing" class="pd-meta">
          <dt>owner</dt>
          <dd>{{ plugin.ownerName }}</dd>
          <template v-if="plugin.authorName">
            <dt>author</dt>
            <dd>{{ plugin.authorName }}</dd>
          </template>
          <template v-if="plugin.authorEmail">
            <dt>email</dt>
            <dd><a :href="`mailto:${plugin.authorEmail}`">{{ plugin.authorEmail }}</a></dd>
          </template>
          <template v-if="plugin.homepage">
            <dt>homepage</dt>
            <dd><a :href="plugin.homepage" target="_blank" rel="noopener noreferrer">{{ plugin.homepage }} ↗</a></dd>
          </template>
          <template v-if="plugin.license">
            <dt>license</dt>
            <dd>{{ plugin.license }}</dd>
          </template>
          <dt>updated</dt>
          <dd class="pd-meta__dim">{{ fmt(plugin.updatedAt) }}</dd>
        </dl>

        <form v-else class="pd-form" @submit.prevent="saveEdit">
          <p class="pd-form__readonly">
            <span class="pd-form__readonly-label">name</span>
            <code>{{ plugin.name }}</code>
            <span class="pd-form__readonly-hint">slug is permanent — used in URLs and /plugin install</span>
          </p>

          <div class="pd-field">
            <label class="pd-field__label">description</label>
            <input v-model="editForm.description" class="pd-field__input" required />
          </div>

          <div class="pd-field">
            <label class="pd-field__label">license</label>
            <input v-model="editForm.license" class="pd-field__input" placeholder="MIT" />
          </div>

          <div class="pd-field-row">
            <div class="pd-field">
              <label class="pd-field__label">author name</label>
              <input v-model="editForm.authorName" class="pd-field__input" />
            </div>
            <div class="pd-field">
              <label class="pd-field__label">author email</label>
              <input v-model="editForm.authorEmail" type="email" class="pd-field__input" />
            </div>
          </div>

          <div class="pd-field">
            <label class="pd-field__label">homepage</label>
            <input v-model="editForm.homepage" type="url" class="pd-field__input" placeholder="https://example.com" />
          </div>
          <p class="pd-form__note">
            version is managed automatically — it bumps as skills change.
          </p>

          <ErrorAlert :message="editError" />

          <div class="pd-form__actions">
            <button type="button" class="pd-btn" :disabled="saving" @click="cancelEdit">cancel</button>
            <button type="submit" class="pd-btn pd-btn--primary" :disabled="saving">
              {{ saving ? 'saving…' : 'save' }}
            </button>
          </div>
        </form>
      </div>
    </section>
  </div>
</template>

<style scoped>
.pd {
  margin-top: -16px;
}

.pd-loading {
  font-family: var(--mono);
  font-size: 12.5px;
  color: var(--muted);
  margin: 0;
}

/* ─── Identity bar (matches skill edit) ─────────────────────────── */
.pd-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  padding: 14px 16px;
  margin: 0 -16px 0;
  background: var(--bg);
  border-top: 1px solid var(--border-soft);
  border-bottom: 1px solid var(--border);
}
.pd-bar__id {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1 1 auto;
}
.pd-bar__kind {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  color: var(--accent);
  padding: 3px 8px;
  border: 1px solid var(--accent);
  background: transparent;
}
.pd-bar__divider {
  width: 1px;
  height: 16px;
  background: var(--border);
}
.pd-bar__path {
  font-family: var(--mono);
  font-size: 14px;
  color: var(--text);
  background: transparent;
  border: 0;
  padding: 0;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.pd-bar__ver {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--accent-2);
  padding: 2px 9px;
  border: 1px solid var(--border);
  background: var(--bg-2);
}
.pd-bar__actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.pd-desc {
  margin: 14px 0 22px;
  font-family: var(--mono);
  font-size: 13.5px;
  line-height: 1.6;
  color: var(--text-soft);
  max-width: 72ch;
}

/* ─── Flat buttons ─────────────────────────────────────────────── */
.pd-btn {
  background: transparent;
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 6px 12px;
  margin: 0;
  font-family: var(--mono);
  font-size: 11.5px;
  font-weight: 500;
  letter-spacing: 0.02em;
  text-transform: lowercase;
  line-height: 1.5;
  cursor: pointer;
  text-decoration: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  transition: border-color 0.12s ease, color 0.12s ease, background 0.12s ease;
}
.pd-btn::before { display: none; content: none; }
.pd-btn:hover {
  background: transparent;
  color: var(--accent);
  border-color: var(--accent);
  transform: none;
}
.pd-btn:active { transform: none; }
.pd-btn:disabled,
.pd-btn:disabled:hover {
  opacity: 0.35;
  cursor: not-allowed;
  color: var(--text-soft);
  border-color: var(--border);
}
.pd-btn--primary {
  color: var(--text);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 700;
}
.pd-btn--primary:hover {
  color: var(--bg);
  background: var(--text);
  border-color: var(--text);
}
.pd-btn--danger {
  color: var(--rust);
  border-color: rgb(var(--rust-rgb) / 0.5);
}
.pd-btn--danger:hover {
  color: var(--text);
  background: var(--rust);
  border-color: var(--rust);
}

/* ─── Tabs ─────────────────────────────────────────────────────── */
.pd-tabs {
  display: flex;
  gap: 0;
  margin: 0 0 20px;
  border-bottom: 1px solid var(--border);
}
.pd-tab {
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  padding: 12px 18px;
  margin-bottom: -1px;
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 500;
  letter-spacing: 0.02em;
  text-transform: none;
  line-height: 1.4;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  transition: color 0.12s ease, border-color 0.12s ease;
}
.pd-tab::before { display: none; content: none; }
.pd-tab:hover { color: var(--text); transform: none; background: transparent; }
.pd-tab--active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.pd-tab__count {
  font-size: 10.5px;
  color: var(--muted);
  letter-spacing: 0;
}

/* ─── Toolbar (above skills table) ─────────────────────────────── */
.pd-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 0 0 12px;
  flex-wrap: wrap;
}
.pd-toolbar__count {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
}

/* ─── Empty state ──────────────────────────────────────────────── */
.pd-empty {
  padding: 22px 24px;
  border: 1px dashed var(--border);
  background: var(--bg-2);
}
.pd-empty__line {
  margin: 0 0 6px;
  font-family: var(--mono);
  font-size: 14px;
  color: var(--text);
  letter-spacing: 0.02em;
}
.pd-empty__prompt {
  color: var(--accent);
  margin-right: 8px;
  font-weight: 700;
}
.pd-empty__hint {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}

/* ─── Tables (matches portal page) ─────────────────────────────── */
.pd-table {
  width: 100%;
  border-collapse: collapse;
  border: 1px solid var(--border);
  background: var(--bg-2);
  margin: 0 0 24px;
  font-family: var(--mono);
}
.pd-table th {
  text-align: left;
  padding: 9px 14px;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  background: var(--bg);
}
.pd-table td {
  padding: 11px 14px;
  border-bottom: 1px solid var(--border-soft);
  font-size: 13px;
  color: var(--text);
  vertical-align: top;
}
.pd-table tbody tr:last-child td { border-bottom: 0; }
.pd-table tbody tr {
  transition: background 0.12s ease;
}
.pd-table tbody tr:hover {
  background: rgb(var(--accent-rgb) / 0.04);
}
.pd-table__name {
  width: 25%;
  overflow-wrap: anywhere;
}
.pd-table__name a {
  color: var(--text);
  border-bottom: 1px solid var(--accent);
  padding-bottom: 1px;
  font-weight: 600;
  transition: color 0.12s ease;
}
.pd-table__name a:hover { color: var(--accent); }
.pd-table__desc { color: var(--text-soft); }
.pd-table__when {
  color: var(--muted);
  font-size: 11.5px;
  white-space: nowrap;
}
.pd-table__when-time { display: block; font-size: 10.5px; }
.pd-table__act { text-align: right; width: 1%; white-space: nowrap; }
.pd-table--nested { margin: 0; }

/* ─── Disclosure ───────────────────────────────────────────────── */
.pd-disclosure {
  margin-top: 14px;
}
.pd-disclosure__head {
  cursor: pointer;
  list-style: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  background: var(--bg-2);
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  text-transform: uppercase;
  color: var(--text-soft);
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
  user-select: none;
}
.pd-disclosure__head::-webkit-details-marker { display: none; }
.pd-disclosure__toggle {
  display: inline-grid;
  place-items: center;
  width: 18px;
  height: 18px;
  border: 1px solid var(--border);
  color: var(--accent);
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0;
  line-height: 1;
  flex: 0 0 auto;
  transition: border-color 0.15s ease;
}
.pd-disclosure:not([open]) > .pd-disclosure__head .pd-disclosure__toggle::before { content: '+'; }
.pd-disclosure[open] > .pd-disclosure__head .pd-disclosure__toggle::before { content: '−'; }
.pd-disclosure__title { letter-spacing: inherit; flex: 0 0 auto; }
.pd-disclosure__count {
  font-family: var(--mono);
  font-size: 10.5px;
  letter-spacing: 0.08em;
  text-transform: lowercase;
  color: var(--muted);
  font-weight: 500;
}
.pd-disclosure__hint {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--muted);
}
.pd-disclosure__hint-open,
.pd-disclosure__hint-close { display: none; }
.pd-disclosure:not([open]) > .pd-disclosure__head .pd-disclosure__hint-open { display: inline; }
.pd-disclosure[open] > .pd-disclosure__head .pd-disclosure__hint-close { display: inline; }
.pd-disclosure__chev {
  display: inline-block;
  color: var(--accent);
  font-size: 12px;
  transition: transform 0.18s ease;
  letter-spacing: 0;
}
.pd-disclosure[open] > .pd-disclosure__head .pd-disclosure__chev { transform: rotate(90deg); }
.pd-disclosure__head:hover {
  color: var(--text);
  border-color: var(--accent);
  background: rgb(var(--accent-rgb) / 0.04);
}
.pd-disclosure__head:hover .pd-disclosure__toggle { border-color: var(--accent); }
.pd-disclosure__head:hover .pd-disclosure__hint { color: var(--text-soft); }
.pd-disclosure[open] > .pd-disclosure__head {
  color: var(--text);
  border-bottom-color: var(--accent);
  margin-bottom: 12px;
}
.pd-disclosure__note {
  margin: 0 0 10px;
  font-size: 11.5px;
  color: var(--muted);
}

/* ─── Connect blocks ───────────────────────────────────────────── */
.pd-block {
  margin: 0 0 28px;
  padding: 0 0 0 16px;
  border-left: 2px solid var(--border);
}
.pd-block__head { margin-bottom: 8px; }
.pd-block__title {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  text-transform: uppercase;
  color: var(--accent);
}
.pd-block__body {
  margin: 0 0 12px;
  font-size: 12.5px;
  color: var(--text-soft);
  line-height: 1.55;
}

/* ─── Code block ───────────────────────────────────────────────── */
.pd-code {
  margin: 0 0 8px;
}
.pd-code pre {
  margin: 0;
  padding: 12px 14px 12px 22px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-left: 2px solid var(--accent);
  border-radius: 0;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--text);
  white-space: pre-wrap;
  word-break: break-all;
}
.pd-code pre::before { content: none; }
.pd-code-actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

/* ─── Metadata list ────────────────────────────────────────────── */
.pd-meta {
  display: grid;
  grid-template-columns: 110px 1fr;
  gap: 6px 18px;
  margin: 0;
  font-family: var(--mono);
  font-size: 13px;
}
.pd-meta dt {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
  padding-top: 3px;
}
.pd-meta dd {
  margin: 0;
  color: var(--text);
  word-break: break-word;
}
.pd-meta dd a {
  color: var(--text);
  border-bottom: 1px solid var(--accent);
  padding-bottom: 1px;
  transition: color 0.12s ease;
}
.pd-meta dd a:hover { color: var(--accent); }
.pd-meta__dim { color: var(--muted); }

/* ─── Edit form ────────────────────────────────────────────────── */
.pd-block__editing {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 500;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--accent-2);
  margin-left: 8px;
}
.pd-form { display: block; }
.pd-form__readonly {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 10px;
  margin: 0 0 4px;
  font-family: var(--mono);
  font-size: 12px;
}
.pd-form__readonly-label {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--muted);
}
.pd-form__readonly code {
  font-size: 12.5px;
  color: var(--text);
  background: var(--bg-2);
  border: 1px dashed var(--border);
  padding: 2px 8px;
}
.pd-form__readonly-hint {
  font-size: 11px;
  color: var(--muted);
}
.pd-field {
  display: block;
  margin-top: 18px;
  flex: 1 1 200px;
  min-width: 0;
}
.pd-field__label {
  display: block;
  margin: 0 0 6px;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.pd-field__input {
  width: 100%;
  background: var(--bg-2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 8px 12px;
  font-family: var(--mono);
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s ease;
}
.pd-field__input:focus { border-color: var(--accent); }
.pd-field__input::placeholder { color: var(--muted); }
.pd-field__hint {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--muted);
  letter-spacing: 0.02em;
}
.pd-field-row {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  margin-top: 0;
}
.pd-form__actions {
  display: flex;
  gap: 6px;
  margin-top: 18px;
}
.pd-form__note {
  margin: 16px 0 0;
  padding: 8px 12px;
  font-size: 11.5px;
  color: var(--muted);
  background: var(--bg-2);
  border-left: 2px solid var(--border);
  line-height: 1.5;
}
@media (max-width: 720px) {
  .pd-field-row { gap: 8px; }
}

@media (max-width: 720px) {
  .pd-bar { padding: 12px; }
  .pd-tab { padding: 10px 12px; }
  .pd-block { padding-left: 12px; }
  .pd-code pre { padding: 10px 12px 10px 18px; font-size: 12px; }
  .pd-meta { grid-template-columns: 90px 1fr; gap: 6px 12px; }
}
</style>
