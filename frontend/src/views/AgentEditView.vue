<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import { api, errMsg, errStatus, slugError } from '../api'
import type { AgentInput } from '../types'
import ErrorAlert from '../components/ErrorAlert.vue'
import ErrorView from './ErrorView.vue'
import { useAuthStore } from '../stores/auth'
import { usePluginStore } from '../stores/plugins'
import { useConfirm } from '../composables/useConfirm'

// agentName === null means "create a new agent in this plugin".
const props = defineProps<{ pluginName: string; agentName: string | null }>()

const router = useRouter()
const auth = useAuthStore()
const pluginStore = usePluginStore()
const { confirm } = useConfirm()

const isNew = computed(() => props.agentName === null)

const name = ref('')
const description = ref('')
const model = ref('')
const tools = ref('')
const body = ref('')
const extraFrontmatter = ref('')

const loading = ref(true)
const saving = ref(false)
const error = ref('')
const nameError = ref('')
// A failed initial load (missing plugin or agent) takes over the whole view.
const loadErrorCode = ref<number | null>(null)

// Who may delete mirrors the server rule: plugin owner, admin, or the agent's
// creator. The server enforces it; this only decides whether to show the button.
const ownerId = ref('')
const createdBy = ref('')
// A locked agent is withdrawn from git and MCP. The server refuses edits, and
// only an admin may delete it, so the form goes read-only.
const locked = ref(false)
const lockNote = ref('')
// Audit gate: the stored content is not published until the check passes.
const pending = ref(false)
const pendingError = ref('')
const canDelete = computed(() => {
  const u = auth.user
  if (!u || isNew.value) return false
  if (locked.value) return u.isAdmin
  return u.isAdmin || u.id === ownerId.value || u.id === createdBy.value
})

// The value a user passes as subagent_type in Claude Code.
const agentType = computed(() => `${props.pluginName}:${name.value || '<name>'}`)

const MODEL_SUGGESTIONS = ['inherit', 'opus', 'sonnet', 'haiku']

async function load() {
  loading.value = true
  error.value = ''
  loadErrorCode.value = null
  try {
    const plugin = await pluginStore.loadPlugin(props.pluginName)
    ownerId.value = plugin.ownerId
    if (isNew.value) {
      name.value = ''
      description.value = ''
      model.value = ''
      tools.value = ''
      body.value = ''
      extraFrontmatter.value = ''
      createdBy.value = ''
      locked.value = false
      lockNote.value = ''
      pending.value = false
      pendingError.value = ''
      return
    }
    const agent = plugin.agents?.find(a => a.name === props.agentName)
    if (!agent) {
      error.value = 'agent not found'
      loadErrorCode.value = 404
      return
    }
    name.value = agent.name
    description.value = agent.description
    model.value = agent.model
    tools.value = agent.tools
    body.value = agent.body
    extraFrontmatter.value = agent.extraFrontmatter
    createdBy.value = agent.createdBy ?? ''
    locked.value = agent.locked
    pending.value = !!agent.auditPending
    pendingError.value = agent.auditPendingError ?? ''
    const who = agent.lockSource === 'audit' ? 'the security audit' : (agent.lockedByName || 'an admin')
    lockNote.value = agent.locked
      ? `Locked by ${who}${agent.lockReason ? ` — ${agent.lockReason}` : ''}`
      : ''
  } catch (e: unknown) {
    error.value = errMsg(e)
    loadErrorCode.value = errStatus(e) ?? 500
  } finally {
    loading.value = false
  }
}

async function save() {
  error.value = ''
  nameError.value = ''
  if (isNew.value) {
    const slugErr = slugError(name.value)
    if (slugErr) {
      nameError.value = slugErr
      return
    }
  }
  if (!description.value.trim()) {
    error.value = 'description is required'
    return
  }
  if (!body.value.trim()) {
    error.value = 'system prompt is required'
    return
  }
  // Always send model, tools and extraFrontmatter so that clearing a field in
  // the form clears it on the server (a missing key would keep the old value).
  const data: AgentInput = {
    description: description.value,
    body: body.value,
    model: model.value.trim(),
    tools: tools.value.trim(),
    extraFrontmatter: extraFrontmatter.value,
  }
  saving.value = true
  try {
    if (isNew.value) {
      await api.createAgent(props.pluginName, { ...data, name: name.value })
    } else {
      await api.updateAgent(props.pluginName, name.value, data)
    }
    router.push(`/plugins/${props.pluginName}`)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  const ok = await confirm({
    title: 'Delete agent',
    message: `Delete agent "${name.value}"? This is permanent: agents have no version history and cannot be restored. Anything that starts "${agentType.value}" will stop working.`,
    confirmLabel: 'Delete agent',
    danger: true,
  })
  if (!ok) return
  saving.value = true
  try {
    await api.deleteAgent(props.pluginName, name.value)
    router.push(`/plugins/${props.pluginName}`)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  load()
})
// Vue Router reuses this component between agents of the same plugin.
watch(() => [props.pluginName, props.agentName], load)
</script>

<template>
  <p v-if="loading" class="muted">loading…</p>
  <ErrorView
    v-else-if="loadErrorCode !== null"
    :code="loadErrorCode"
    :title="loadErrorCode === 404 ? 'Agent not found' : undefined"
    :details="error"
  />

  <div v-else>
    <h1>{{ isNew ? 'New agent' : 'Edit agent' }}</h1>
    <p class="ae-lede">
      An agent is a subagent that ships with the plugin. It is published as
      <code>agents/{{ name || '&lt;name&gt;' }}.md</code>, and Claude Code exposes it as
      the agent type <code>{{ agentType }}</code>.
    </p>

    <p v-if="locked" class="ae-locked" role="status">
      🔒 {{ lockNote }}. This agent is withdrawn from git and MCP and cannot be
      edited. An admin can unlock it on the plugin page.
    </p>

    <p v-else-if="pending" class="ae-pending" role="status">
      <template v-if="pendingError">
        ⏸ The security check for the latest change failed ({{ pendingError }}), so
        that change is not published. The check is retried automatically, and an
        admin can publish it from the plugin page.
      </template>
      <template v-else>
        ⏳ The latest change is saved but not published yet. It goes live when the
        security check passes.
      </template>
      The last published version stays live until then.
    </p>

    <div class="card">
      <form @submit.prevent="save">
        <label for="ae-name">Name (slug, lowercase, [a-z0-9-])</label>
        <input
          id="ae-name"
          v-model="name"
          :readonly="!isNew"
          required
          :class="{ 'input--invalid': nameError }"
          :aria-invalid="nameError ? 'true' : undefined"
          @input="nameError = ''"
        />
        <p v-if="nameError" class="field-error">{{ nameError }}</p>
        <p v-else-if="!isNew" class="ae-hint">the name is permanent — it is part of the agent type.</p>

        <label for="ae-description">Description</label>
        <textarea
          id="ae-description"
          v-model="description"
          class="ae-textarea--short"
          rows="3"
          required
          placeholder="When should Claude hand a task to this agent?"
        ></textarea>

        <div class="row ae-row">
          <div class="ae-col">
            <label for="ae-model">Model (optional)</label>
            <input
              id="ae-model"
              v-model="model"
              list="ae-model-suggestions"
              placeholder="inherit"
              autocomplete="off"
            />
            <datalist id="ae-model-suggestions">
              <option v-for="m in MODEL_SUGGESTIONS" :key="m" :value="m" />
            </datalist>
            <p class="ae-hint">an alias (opus, sonnet, haiku, inherit) or a full model id. empty = claude code default.</p>
          </div>
          <div class="ae-col">
            <label for="ae-tools">Tools (optional)</label>
            <input id="ae-tools" v-model="tools" placeholder="Read, Grep, Bash" autocomplete="off" />
            <p class="ae-hint">comma-separated allowlist. empty = the agent gets every tool.</p>
          </div>
        </div>

        <label for="ae-body">System prompt</label>
        <textarea
          id="ae-body"
          v-model="body"
          rows="16"
          required
          placeholder="You are …"
        ></textarea>

        <details class="ae-extra" :open="!!extraFrontmatter">
          <summary>more frontmatter (advanced)</summary>
          <label for="ae-extra">Extra frontmatter (raw YAML)</label>
          <textarea
            id="ae-extra"
            v-model="extraFrontmatter"
            class="ae-textarea--short"
            rows="4"
            placeholder="color: blue"
          ></textarea>
          <p class="ae-hint">
            further keys, written into the file as-is. do not repeat name, description, model or tools here.
          </p>
        </details>

        <ErrorAlert :message="error" />

        <div class="row ae-actions">
          <button type="submit" :disabled="saving || locked">
            {{ saving ? 'Saving…' : (isNew ? 'Create agent' : 'Save agent') }}
          </button>
          <RouterLink :to="`/plugins/${pluginName}`" class="btn secondary">Cancel</RouterLink>
          <span class="spacer"></span>
          <button
            v-if="canDelete"
            type="button"
            class="danger"
            :disabled="saving"
            @click="remove"
          >Delete agent</button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.ae-lede {
  margin: 0 0 18px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-soft);
  max-width: 78ch;
}
.ae-locked {
  margin: 0 0 18px;
  padding: 10px 14px;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--rust);
  border: 1px solid rgb(var(--rust-rgb) / 0.5);
  background: rgb(var(--rust-rgb) / 0.06);
}
.ae-pending {
  margin: 0 0 18px;
  padding: 10px 14px;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--text-soft);
  border: 1px solid var(--border);
  background: var(--bg-2);
}
.ae-row {
  align-items: flex-start;
  flex-wrap: wrap;
}
.ae-col {
  flex: 1 1 260px;
  min-width: 0;
}
.ae-hint {
  margin: 6px 0 0;
  font-size: 11.5px;
  color: var(--muted);
  font-family: var(--mono);
}
.ae-textarea--short {
  min-height: 0;
}
.ae-extra {
  margin-top: 22px;
}
.ae-extra > summary {
  cursor: pointer;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.ae-actions {
  margin-top: 20px;
  flex-wrap: wrap;
}
.input--invalid {
  border-color: var(--rust);
}
.field-error {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--rust);
  font-family: var(--mono);
}
</style>
