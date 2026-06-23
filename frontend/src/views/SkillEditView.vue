<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch, nextTick } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRouter } from 'vue-router'
import { api, errMsg, slugError } from '../api'
import type { ValidationReport, FindingSeverity, Finding } from '../types'
import { useConfirm } from '../composables/useConfirm'
import {
  useSkillFileManager,
  fmtBytes,
  FOLDER_HINT,
  isWellKnownFolder,
  isRootFolder,
  folderLabel,
  fileDisplayName,
} from '../composables/useSkillFileManager'
import SkillVersionHistory from '../components/SkillVersionHistory.vue'
import ErrorAlert from '../components/ErrorAlert.vue'
import MarkdownEditor from '../components/MarkdownEditor.vue'
import { usePluginStore } from '../stores/plugins'
import { useAuthStore } from '../stores/auth'
import { usePrompt } from '../composables/usePrompt'

const pluginStore = usePluginStore()
const auth = useAuthStore()

const { confirm } = useConfirm()
const { prompt } = usePrompt()
const isAdmin = computed(() => !!auth.user?.isAdmin)

const props = defineProps<{
  pluginName: string
  skillName: string | null
}>()

const router = useRouter()
const isEdit = computed(() => !!props.skillName)
const name = ref('')
const description = ref('')
const body = ref(defaultBody())
const extraFrontmatter = ref('')
const error = ref('')
const nameError = ref('')
const loading = ref(false)
const importing = ref(false)
const importInput = ref<HTMLInputElement | null>(null)
const versionHistory = ref<InstanceType<typeof SkillVersionHistory> | null>(null)
const validating = ref(false)
const validationReport = ref<ValidationReport | null>(null)
const validationError = ref('')
const reviewSection = ref<HTMLElement | null>(null)
const applyStatus = ref<'idle' | 'applied'>('idle')
let applyResetTimer: number | undefined

// Per-finding apply state. Indexed by position in sortedFindings — the array
// is stable for a given validation report, and we reset on every revalidate.
type FixState = 'idle' | 'loading' | 'applied' | 'error'
const fixStatus = ref<Record<number, FixState>>({})
const fixError = ref<Record<number, string>>({})
const fixNote = ref<Record<number, string>>({})
const fixResetTimers = new Map<number, number>()

// Tabs: SKILL = description+body editor; MORE = supporting files. Most users
// only ever need SKILL, so we default there and keep the file tree out of
// sight until they opt in.
type Tab = 'skill' | 'more'
const tab = ref<Tab>('skill')

const {
  files,
  selectedPath,
  fileContent,
  fileIsBinary,
  fileSize,
  fileLoading,
  fileDirty,
  fileError,
  filesByFolder,
  folderList,
  uploadInput,
  loadFiles,
  selectFile,
  saveCurrentFile,
  deleteCurrentFile,
  downloadCurrentFile,
  promptNewFile,
  triggerUpload,
  onUploadChange,
  onDrop,
  refreshAfterRevert,
} = useSkillFileManager(
  () => props.pluginName,
  () => props.skillName,
  { onChanged: () => versionHistory.value?.reload() },
)

const SEVERITY_ORDER: Record<FindingSeverity, number> = {
  problem: 0,
  warning: 1,
  info: 2,
}

const selectedFileIsMarkdown = computed(() => {
  const p = selectedPath.value
  return !!p && /\.md$/i.test(p)
})

const sortedFindings = computed(() => {
  if (!validationReport.value) return []
  return [...validationReport.value.findings].sort(
    (a, b) => SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity],
  )
})

const findingCounts = computed(() => {
  const counts = { problem: 0, warning: 0, info: 0 } as Record<FindingSeverity, number>
  for (const f of validationReport.value?.findings ?? []) counts[f.severity]++
  return counts
})

function applySuggestedDescription() {
  if (validationReport.value?.suggestedDescription) {
    description.value = validationReport.value.suggestedDescription
    markTouched()
    applyStatus.value = 'applied'
    if (applyResetTimer) window.clearTimeout(applyResetTimer)
    applyResetTimer = window.setTimeout(() => { applyStatus.value = 'idle' }, 1800)
  }
}

function resetFindingFixState() {
  fixStatus.value = {}
  fixError.value = {}
  fixNote.value = {}
  for (const id of fixResetTimers.values()) window.clearTimeout(id)
  fixResetTimers.clear()
}

async function applyFindingFix(finding: Finding, idx: number) {
  fixStatus.value = { ...fixStatus.value, [idx]: 'loading' }
  fixError.value = { ...fixError.value, [idx]: '' }
  fixNote.value = { ...fixNote.value, [idx]: '' }
  try {
    const fix = await api.fixFinding({
      pluginName: props.pluginName,
      skillName: props.skillName ?? undefined,
      name: name.value,
      description: description.value,
      body: body.value,
      extraFrontmatter: extraFrontmatter.value,
      files: files.value,
      finding,
    })
    let changed = false
    // name is the directory key in edit mode — never overwrite it post-creation.
    if (typeof fix.name === 'string' && !isEdit.value) {
      name.value = fix.name
      changed = true
    }
    if (typeof fix.description === 'string') {
      description.value = fix.description
      changed = true
    }
    if (typeof fix.body === 'string') {
      body.value = fix.body
      changed = true
    }
    if (typeof fix.extraFrontmatter === 'string') {
      extraFrontmatter.value = fix.extraFrontmatter
      changed = true
    }
    if (changed) markTouched()
    fixStatus.value = { ...fixStatus.value, [idx]: changed ? 'applied' : 'error' }
    if (!changed) {
      fixError.value = { ...fixError.value, [idx]: 'no changes returned' }
    } else if (fix.note) {
      fixNote.value = { ...fixNote.value, [idx]: fix.note }
    }
    const existing = fixResetTimers.get(idx)
    if (existing) window.clearTimeout(existing)
    if (changed) {
      const timer = window.setTimeout(() => {
        if (fixStatus.value[idx] === 'applied') {
          fixStatus.value = { ...fixStatus.value, [idx]: 'idle' }
        }
        fixResetTimers.delete(idx)
      }, 2400)
      fixResetTimers.set(idx, timer)
    }
  } catch (e: unknown) {
    fixStatus.value = { ...fixStatus.value, [idx]: 'error' }
    fixError.value = { ...fixError.value, [idx]: errMsg(e) }
  }
}
const audit = ref<{
  createdByName?: string
  createdAt?: string
  updatedByName?: string
  updatedAt?: string
}>({})

// Lock state of the loaded skill. A locked skill is withdrawn from git/MCP and
// is read-only here: every mutating action is disabled and the server would
// reject it with 403 anyway. Only an admin can lock or unlock.
const lock = ref<{
  locked: boolean
  reason?: string
  source?: 'admin' | 'audit'
  byName?: string
}>({ locked: false })
const locked = computed(() => lock.value.locked)
const lockBusy = ref(false)

// Pristine snapshot of the editable fields. Updated whenever the form's
// contents match what's persisted on the server (initial load, after save,
// after revert) so isDirty correctly flips back to false.
const pristine = ref({
  description: '',
  body: defaultBody(),
  extraFrontmatter: '',
})

// Set true on any real user input event. The MarkdownEditor (Crepe) emits
// `update:modelValue` for its own post-mount normalization, not just for
// typing — so we can't rely on `body !== pristine.body` alone. A keyboard
// or input event from the user is the authoritative dirty signal.
const userTouched = ref(false)
function markTouched() { userTouched.value = true }

function snapshotPristine() {
  pristine.value = {
    description: description.value,
    body: body.value,
    extraFrontmatter: extraFrontmatter.value,
  }
  userTouched.value = false
}

const isDirty = computed(() => {
  if (fileDirty.value) return true
  if (!userTouched.value) return false
  return description.value !== pristine.value.description
    || body.value !== pristine.value.body
    || extraFrontmatter.value !== pristine.value.extraFrontmatter
})

// Set true just before navigations the user explicitly initiated (Save,
// Cancel, Import, …). The route guard checks this and skips the discard
// prompt, since the action itself implies intent.
let bypassGuard = false

function defaultBody() {
  return `## Instructions

Describe what the skill does, step by step.
`
}

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

async function load() {
  if (!isEdit.value) {
    snapshotPristine()
    return
  }
  try {
    const p = await pluginStore.loadPlugin(props.pluginName)
    const s = p.skills?.find(s => s.name === props.skillName)
    if (!s) {
      error.value = 'skill not found'
      return
    }
    name.value = s.name
    description.value = s.description
    body.value = s.body
    extraFrontmatter.value = s.extraFrontmatter ?? ''
    audit.value = {
      createdByName: s.createdByName,
      createdAt: s.createdAt,
      updatedByName: s.updatedByName,
      updatedAt: s.updatedAt,
    }
    lock.value = {
      locked: s.locked,
      reason: s.lockReason,
      source: s.lockSource,
      byName: s.lockedByName,
    }
    snapshotPristine()
    await loadFiles()
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

function triggerImport() {
  importInput.value?.click()
}

async function onImportFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  error.value = ''
  importing.value = true
  try {
    const s = await api.importSkill(props.pluginName, file)
    bypassGuard = true
    router.push(`/plugins/${props.pluginName}/skills/${s.name}/edit`)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    importing.value = false
  }
}

async function submit() {
  error.value = ''
  nameError.value = ''
  if (!isEdit.value) {
    const slugErr = slugError(name.value)
    if (slugErr) {
      nameError.value = slugErr
      return
    }
  }
  loading.value = true
  try {
    if (isEdit.value) {
      await api.updateSkill(props.pluginName, props.skillName!, {
        description: description.value,
        body: body.value,
        extraFrontmatter: extraFrontmatter.value,
      })
    } else {
      await api.createSkill(props.pluginName, {
        name: name.value,
        description: description.value,
        body: body.value,
        extraFrontmatter: extraFrontmatter.value,
      })
    }
    bypassGuard = true
    router.push(`/plugins/${props.pluginName}`)
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

function cancel() {
  // Cancel is an explicit, intentional discard — skip the unsaved-changes
  // prompt for it. The route guard only fires for accidental leaves
  // (breadcrumb, browser back, etc.).
  bypassGuard = true
  router.push(`/plugins/${props.pluginName}`)
}

async function deleteSkill() {
  if (!props.skillName) return
  const ok = await confirm({
    title: 'Delete skill',
    message: `Delete skill "${props.skillName}"? You can restore it later from the Deleted skills section on the plugin page.`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteSkill(props.pluginName, props.skillName)
    bypassGuard = true
    router.push(`/plugins/${props.pluginName}`)
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

// ─── Lock / unlock (admin only) ────────────────────────────────────
// Locking withdraws the skill from git, the external mirror, and MCP; it stays
// visible here, read-only. Unlocking restores it. Both reload the skill so the
// banner and disabled-state update.
async function lockSkill() {
  if (!props.skillName) return
  const reason = await prompt({
    title: `Lock skill "${props.skillName}"`,
    message: 'Locking withdraws this skill from git, the external mirror, and MCP. It stays visible here, marked as locked. Add an optional reason:',
    placeholder: 'e.g. under security review',
    confirmLabel: 'Lock skill',
  })
  if (reason === null) return
  lockBusy.value = true
  try {
    await api.lockSkill(props.pluginName, props.skillName, reason)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    lockBusy.value = false
  }
}

async function unlockSkill() {
  if (!props.skillName) return
  const ok = await confirm({
    title: `Unlock skill "${props.skillName}"`,
    message: 'This restores the skill to git, the external mirror, and MCP. If the audit locked it automatically, future audit runs will not re-lock it.',
    confirmLabel: 'Unlock',
  })
  if (!ok) return
  lockBusy.value = true
  try {
    await api.unlockSkill(props.pluginName, props.skillName)
    await load()
  } catch (e: unknown) {
    error.value = errMsg(e)
  } finally {
    lockBusy.value = false
  }
}

// ─── Move skill to another plugin ──────────────────────────────────
// A move is client-visible: anything referencing the skill at its current
// <plugin>/<skill> path breaks until it's updated. So we gate it behind an
// explicit modal (not a native confirm) that names the consequence and makes
// the user pick the destination deliberately.
const moveOpen = ref(false)
const moveTarget = ref('')
const moving = ref(false)
const moveError = ref('')

// Active plugins other than the one this skill currently lives in.
const moveCandidates = computed(() =>
  pluginStore.list.filter(p => p.name !== props.pluginName),
)

async function openMove() {
  moveError.value = ''
  moveTarget.value = ''
  moveOpen.value = true
  try {
    await pluginStore.loadList()
  } catch (e: unknown) {
    moveError.value = errMsg(e)
  }
}

function closeMove() {
  if (moving.value) return
  moveOpen.value = false
}

async function confirmMove() {
  if (!props.skillName || !moveTarget.value) return
  moving.value = true
  moveError.value = ''
  try {
    const dest = moveTarget.value
    await api.moveSkill(props.pluginName, props.skillName, dest)
    moveOpen.value = false
    // The skill kept its name, just changed home — land the user on it in the
    // target plugin. bypassGuard so the unsaved-changes prompt doesn't fire.
    bypassGuard = true
    router.push(`/plugins/${dest}/skills/${props.skillName}/edit`)
  } catch (e: unknown) {
    moveError.value = errMsg(e)
  } finally {
    moving.value = false
  }
}

async function validate() {
  validationError.value = ''
  validationReport.value = null
  resetFindingFixState()
  validating.value = true
  // Section becomes visible on `validating`; wait a tick then scroll so the
  // loading state (and later the results) is in view without the user hunting
  // for it at the bottom of the page.
  await nextTick()
  reviewSection.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  try {
    validationReport.value = await api.validateSkill({
      pluginName: props.pluginName,
      skillName: props.skillName ?? undefined,
      name: name.value,
      description: description.value,
      body: body.value,
      files: files.value,
    })
  } catch (e: unknown) {
    validationError.value = errMsg(e)
  } finally {
    validating.value = false
  }
}

async function revert(version: number) {
  if (!props.skillName) return
  const ok = await confirm({
    title: `Revert to version ${version}`,
    message: 'This restores the description, body, and supporting files from that version, and creates a new history entry. Continue?',
    confirmLabel: 'Revert',
  })
  if (!ok) return
  try {
    const s = await api.revertSkill(props.pluginName, props.skillName, version)
    description.value = s.description
    body.value = s.body
    extraFrontmatter.value = s.extraFrontmatter ?? ''
    audit.value = {
      createdByName: s.createdByName,
      createdAt: s.createdAt,
      updatedByName: s.updatedByName,
      updatedAt: s.updatedAt,
    }
    await Promise.all([versionHistory.value?.reload(), refreshAfterRevert()])
    snapshotPristine()
  } catch (e: unknown) {
    error.value = errMsg(e)
  }
}

// Shared unsaved-changes gate. onBeforeRouteLeave fires when navigating away
// from the component; onBeforeRouteUpdate fires when only the route params
// change (e.g. editing skill A then jumping to skill B), which reuses this
// instance — without the update guard that switch would silently discard the
// in-progress edit before the watch() reloads.
async function confirmDiscardIfDirty(): Promise<boolean> {
  if (bypassGuard) {
    bypassGuard = false
    return true
  }
  if (!isDirty.value) return true
  return await confirm({
    title: 'Discard unsaved changes?',
    message: 'You have unsaved changes to this skill. Leave this page and lose them?',
    confirmLabel: 'Discard',
    cancelLabel: 'Stay',
    danger: true,
  })
}

onBeforeRouteLeave(confirmDiscardIfDirty)
onBeforeRouteUpdate(confirmDiscardIfDirty)

function onBeforeUnload(e: BeforeUnloadEvent) {
  if (!isDirty.value) return
  e.preventDefault()
  // Some browsers still require a returnValue to trigger the native prompt.
  e.returnValue = ''
}

onMounted(() => {
  window.addEventListener('beforeunload', onBeforeUnload)
  load()
})
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  if (applyResetTimer) window.clearTimeout(applyResetTimer)
  for (const id of fixResetTimers.values()) window.clearTimeout(id)
  fixResetTimers.clear()
})
// Same component backs /skills/new and /skills/:name/edit, so a route change
// (e.g. just after import, or switching to a skill in another plugin) reuses
// the instance and skips onMounted — reload explicitly when either the target
// plugin or skill changes, not just the skill name.
watch(() => [props.pluginName, props.skillName], load)
</script>

<template>
  <div class="se">
    <!-- Action bar: identity + primary actions, sticky under nav -->
    <header class="se-bar">
      <div class="se-bar__id">
        <span class="se-bar__kind">{{ isEdit ? 'EDIT' : 'NEW' }}</span>
        <span class="se-bar__divider"></span>
        <code class="se-bar__path">
          {{ pluginName }}/<span class="se-bar__leaf">{{ isEdit ? skillName : '…' }}</span>
        </code>
        <span v-if="isDirty" class="se-bar__state" title="Unsaved changes">
          <span class="se-bar__dot"></span>unsaved
        </span>
      </div>
      <div class="se-bar__actions">
        <button
          v-if="isEdit && isAdmin && !locked"
          type="button"
          class="se-btn"
          :disabled="lockBusy"
          @click="lockSkill"
        >{{ lockBusy ? 'locking…' : 'lock' }}</button>
        <button
          v-if="isEdit && isAdmin && locked"
          type="button"
          class="se-btn se-btn--unlock"
          :disabled="lockBusy"
          @click="unlockSkill"
        >{{ lockBusy ? 'unlocking…' : 'unlock' }}</button>
        <button
          v-if="isEdit"
          type="button"
          class="se-btn se-btn--danger"
          :disabled="locked && !isAdmin"
          :title="locked && isAdmin ? 'Delete this locked skill (admin)' : undefined"
          @click="deleteSkill"
        >delete</button>
        <button
          v-if="isEdit"
          type="button"
          class="se-btn"
          :disabled="locked"
          @click="openMove"
        >move</button>
        <button
          type="button"
          class="se-btn"
          @click="cancel"
        >cancel</button>
        <button
          type="button"
          class="se-btn"
          :disabled="validating || (!description && !body)"
          @click="validate"
        >{{ validating ? 'validating…' : 'validate' }}</button>
        <button
          type="button"
          class="se-btn se-btn--primary"
          :disabled="loading || importing || locked"
          @click="submit"
        >{{ loading ? 'saving…' : (isEdit ? 'save' : 'create') }}</button>
      </div>
    </header>

    <!-- Locked banner -->
    <div v-if="isEdit && locked" class="se-lock" role="status">
      <span class="se-lock__badge">🔒 locked</span>
      <div class="se-lock__body">
        <p class="se-lock__title">
          This skill is locked{{ lock.source === 'audit' ? ' by the security audit' : (lock.byName ? ` by ${lock.byName}` : ' by an admin') }}
          and is hidden from git, the external mirror, and MCP. It stays visible here, read-only.
        </p>
        <p v-if="lock.reason" class="se-lock__reason">reason: {{ lock.reason }}</p>
        <p class="se-lock__hint">
          {{ isAdmin ? 'Unlock it (top right) to restore access and allow edits.' : 'Only an admin can unlock it.' }}
        </p>
      </div>
    </div>

    <!-- ZIP / .skill import bar (new mode only) -->
    <div v-if="!isEdit" class="se-notice">
      <input
        ref="importInput"
        type="file"
        accept=".zip,.skill,application/zip"
        hidden
        @change="onImportFile"
      />
      <span class="se-notice__text">
        already packaged as a <code>.zip</code> or <code>.skill</code>? imports <code>SKILL.md</code>, <code>scripts/</code>, <code>references/</code>, <code>assets/</code> in one go.
      </span>
      <button
        type="button"
        class="se-btn se-btn--ghost"
        :disabled="importing"
        @click="triggerImport"
      >{{ importing ? 'importing…' : 'import ↗' }}</button>
    </div>

    <!-- Tabs (edit mode only) -->
    <nav v-if="isEdit" class="se-tabs" role="tablist">
      <button
        type="button"
        class="se-tab"
        role="tab"
        :class="{ 'se-tab--active': tab === 'skill' }"
        :aria-selected="tab === 'skill'"
        @click="tab = 'skill'"
      >SKILL.md</button>
      <button
        type="button"
        class="se-tab"
        role="tab"
        :class="{ 'se-tab--active': tab === 'more' }"
        :aria-selected="tab === 'more'"
        @click="tab = 'more'"
      >
        files
        <span class="se-tab__count">[{{ files.length }}]</span>
      </button>
    </nav>

    <!-- SKILL form (new + edit/skill-tab share this) -->
    <form
      v-if="!isEdit || tab === 'skill'"
      class="se-form"
      @submit.prevent="submit"
    >
      <div class="se-field">
        <label class="se-field__label">name</label>
        <div v-if="isEdit" class="se-field__readonly">{{ name }}</div>
        <input
          v-else
          v-model="name"
          required
          pattern="[a-z0-9][a-z0-9\-]{1,62}[a-z0-9]"
          placeholder="my-skill-slug"
          class="se-field__input"
          :class="{ 'se-field__input--invalid': nameError }"
          :aria-invalid="nameError ? 'true' : undefined"
          @input="nameError = ''"
        />
        <p v-if="!isEdit && nameError" class="se-field__error">{{ nameError }}</p>
        <p v-else-if="!isEdit" class="se-field__hint">lowercase letters, digits, hyphens · used as the skill directory name</p>
      </div>

      <div class="se-field">
        <label class="se-field__label">description</label>
        <textarea
          v-model="description"
          required
          rows="3"
          class="se-field__textarea"
          placeholder="One sentence — what does this skill do, and when should Claude reach for it?"
          @input="markTouched"
        />
        <p class="se-field__hint">read by claude to decide when to invoke · keep it terse</p>
      </div>

      <details class="se-field se-field--collapse" :open="!!extraFrontmatter">
        <summary class="se-field__summary">
          <span class="se-field__toggle" aria-hidden="true"></span>
          <span class="se-field__summary-label">extra frontmatter</span>
          <span class="se-field__summary-tag">advanced</span>
          <span class="spacer"></span>
          <span class="se-field__summary-hint" aria-hidden="true">
            <span class="se-field__summary-hint-open">expand</span>
            <span class="se-field__summary-hint-close">collapse</span>
            <span class="se-field__summary-chev">▸</span>
          </span>
        </summary>
        <div class="se-field__collapse-body">
          <p class="se-field__hint">
            YAML lines emitted into <code>SKILL.md</code> between name/description and the closing <code>---</code>.
            Use for keys like <code>allowed-tools</code>, <code>license</code>.
          </p>
          <textarea
            v-model="extraFrontmatter"
            rows="3"
            class="se-field__textarea se-field__textarea--code"
            spellcheck="false"
            placeholder="allowed-tools:&#10;  - Read&#10;  - Edit"
            @input="markTouched"
          />
        </div>
      </details>

      <div class="se-field">
        <label class="se-field__label">
          body <span class="se-field__label-tag">markdown · becomes SKILL.md content</span>
        </label>
        <div class="se-field__editor" @input="markTouched" @keydown="markTouched">
          <MarkdownEditor v-model="body" />
        </div>
      </div>

      <ErrorAlert :message="error" />

      <p v-if="!isEdit" class="se-form__foot">
        supporting files — <code>scripts/</code>, <code>references/</code>, <code>assets/</code> — can be added once the skill exists.
      </p>
    </form>

    <!-- FILES tab (edit mode only) -->
    <div v-else-if="tab === 'more'" class="se-files">
      <input ref="uploadInput" type="file" multiple hidden
             @change="onUploadChange($event)" />

      <aside class="se-tree">
        <p class="se-tree__intro">
          optional supporting files claude can load alongside SKILL.md. most skills don't need any.
        </p>
        <ErrorAlert v-if="!selectedPath && fileError" :message="fileError" />

        <div
          v-for="folder in folderList"
          :key="folder"
          class="se-tree__group"
          @dragover.prevent
          @drop="onDrop(folder, $event)"
        >
          <header class="se-tree__head">
            <span class="se-tree__name">{{ folderLabel(folder) }}</span>
            <span class="se-tree__count">[{{ filesByFolder[folder]?.length ?? 0 }}]</span>
            <span class="spacer"></span>
            <button
              v-if="isWellKnownFolder(folder) || isRootFolder(folder)"
              type="button"
              class="se-tree__act"
              title="New file"
              :disabled="locked"
              @click="promptNewFile(folder)"
            >+ new</button>
            <button
              type="button"
              class="se-tree__act"
              title="Upload files"
              :disabled="locked"
              @click="triggerUpload(folder)"
            >↑ upload</button>
          </header>
          <p v-if="FOLDER_HINT[folder]" class="se-tree__hint">{{ FOLDER_HINT[folder] }}</p>
          <ul class="se-tree__list">
            <li v-for="f in filesByFolder[folder] ?? []" :key="f.path">
              <button
                type="button"
                class="se-tree__item"
                :class="{ 'se-tree__item--active': selectedPath === f.path }"
                @click="selectFile(f.path)"
              >
                <span class="se-tree__chev">{{ selectedPath === f.path ? '▸' : '·' }}</span>
                <span class="se-tree__item-name">{{ fileDisplayName(folder, f.path) }}</span>
                <span class="se-tree__item-meta">{{ f.isBinary ? 'bin' : 'txt' }} · {{ fmtBytes(f.sizeBytes) }}</span>
              </button>
            </li>
            <li v-if="(filesByFolder[folder]?.length ?? 0) === 0" class="se-tree__empty">
              · empty · drop files here
            </li>
          </ul>
        </div>
      </aside>

      <section class="se-pane">
        <div v-if="selectedPath === null" class="se-pane__empty">
          <p class="se-pane__empty-title">no file selected</p>
          <p class="se-pane__empty-hint">
            pick one on the left · or use <code>+ new</code> / <code>↑ upload</code> · drag-drop also works
          </p>
        </div>

        <template v-else>
          <header class="se-pane__head">
            <code class="se-pane__path">{{ selectedPath }}</code>
            <span class="se-pane__meta">{{ fileIsBinary ? 'binary' : 'text' }} · {{ fmtBytes(fileSize) }}</span>
            <span class="spacer"></span>
            <button v-if="!fileLoading" type="button" class="se-btn" @click="downloadCurrentFile">download</button>
            <button v-if="!fileLoading" type="button" class="se-btn se-btn--danger" :disabled="locked" @click="deleteCurrentFile">delete</button>
          </header>

          <p v-if="fileLoading" class="se-pane__loading">loading…</p>
          <ErrorAlert v-else-if="fileError" :message="fileError" />

          <template v-else>
            <div
              v-if="!fileIsBinary && selectedFileIsMarkdown"
              class="se-pane__md"
              @input="fileDirty = true"
              @keydown="fileDirty = true"
            >
              <MarkdownEditor v-model="fileContent" />
            </div>
            <textarea
              v-else-if="!fileIsBinary"
              v-model="fileContent"
              class="se-pane__editor"
              spellcheck="false"
              @input="fileDirty = true"
            />
            <p v-else class="se-pane__binary">
              binary file — cannot edit inline. download or upload a replacement onto
              <code>{{ selectedPath.includes('/') ? selectedPath.split('/')[0] + '/' : 'the root' }}</code>.
            </p>

            <div v-if="!fileIsBinary" class="se-pane__actions">
              <button
                type="button"
                class="se-btn se-btn--primary"
                :disabled="!fileDirty || locked"
                @click="saveCurrentFile"
              >save file</button>
            </div>
          </template>
        </template>
      </section>
    </div>

    <!-- Claude review (after validate) -->
    <section
      v-if="validating || validationError || validationReport"
      ref="reviewSection"
      class="se-section"
    >
      <header class="se-section__head">
        <span class="se-section__title">claude review</span>
      </header>

      <div v-if="validating" class="se-progress" role="status" aria-live="polite">
        <div class="se-progress__bar"><div class="se-progress__fill"></div></div>
        <p class="se-progress__label">
          asking claude to review the skill<span class="se-progress__dots" aria-hidden="true"></span>
        </p>
      </div>
      <ErrorAlert :message="validationError" />

      <template v-if="validationReport">
        <p v-if="validationReport.summary" class="se-review__summary">
          {{ validationReport.summary }}
        </p>

        <div class="se-review__counts">
          <span v-if="findingCounts.problem" class="se-tag se-tag--problem">
            {{ findingCounts.problem }} problem<span v-if="findingCounts.problem !== 1">s</span>
          </span>
          <span v-if="findingCounts.warning" class="se-tag se-tag--warning">
            {{ findingCounts.warning }} warning<span v-if="findingCounts.warning !== 1">s</span>
          </span>
          <span v-if="findingCounts.info" class="se-tag se-tag--info">
            {{ findingCounts.info }} info
          </span>
          <span v-if="!sortedFindings.length" class="se-review__pass">
            · no issues found
          </span>
        </div>

        <ul v-if="sortedFindings.length" class="se-findings">
          <li
            v-for="(f, i) in sortedFindings"
            :key="`${f.severity}:${f.title}:${i}`"
            class="se-finding"
            :class="`se-finding--${f.severity}`"
          >
            <div class="se-finding__head">
              <span class="se-finding__sev">[{{ f.severity }}]</span>
              <span class="se-finding__title">{{ f.title }}</span>
              <span class="spacer"></span>
              <button
                type="button"
                class="se-btn se-finding__apply"
                :class="{
                  'se-btn--applied': fixStatus[i] === 'applied',
                  'se-btn--loading': fixStatus[i] === 'loading',
                }"
                :disabled="fixStatus[i] === 'loading'"
                @click="applyFindingFix(f, i)"
              >
                <template v-if="fixStatus[i] === 'loading'">
                  applying<span class="se-progress__dots" aria-hidden="true"></span>
                </template>
                <template v-else-if="fixStatus[i] === 'applied'">applied ✓</template>
                <template v-else-if="fixStatus[i] === 'error'">retry fix</template>
                <template v-else>apply fix</template>
              </button>
            </div>
            <p class="se-finding__detail">{{ f.detail }}</p>
            <p v-if="fixNote[i]" class="se-finding__note">→ {{ fixNote[i] }}</p>
            <p v-if="fixStatus[i] === 'error' && fixError[i]" class="se-finding__error">
              fix failed: {{ fixError[i] }}
            </p>
          </li>
        </ul>

        <div v-if="validationReport.suggestedDescription" class="se-suggest">
          <div class="se-suggest__body">
            <div class="se-suggest__label">→ suggested description</div>
            <div class="se-suggest__text">{{ validationReport.suggestedDescription }}</div>
          </div>
          <button
            type="button"
            class="se-btn"
            :class="{ 'se-btn--applied': applyStatus === 'applied' }"
            @click="applySuggestedDescription"
          >{{ applyStatus === 'applied' ? 'applied ✓' : 'apply' }}</button>
        </div>
      </template>
    </section>

    <!-- Audit (edit only) -->
    <details v-if="isEdit" class="se-disclosure">
      <summary class="se-disclosure__head">
        <span class="se-disclosure__toggle" aria-hidden="true"></span>
        <span class="se-disclosure__title">audit</span>
        <span class="spacer"></span>
        <span class="se-disclosure__hint" aria-hidden="true">
          <span class="se-disclosure__hint-open">expand</span>
          <span class="se-disclosure__hint-close">collapse</span>
          <span class="se-disclosure__chev">▸</span>
        </span>
      </summary>
      <dl class="se-audit">
        <dt>created</dt>
        <dd>{{ audit.createdByName || '—' }} <span class="se-audit__dim">· {{ fmt(audit.createdAt) }}</span></dd>
        <dt>last edit</dt>
        <dd>{{ audit.updatedByName || '—' }} <span class="se-audit__dim">· {{ fmt(audit.updatedAt) }}</span></dd>
      </dl>
    </details>

    <!-- Version history (edit only) -->
    <SkillVersionHistory
      v-if="isEdit"
      ref="versionHistory"
      :plugin-name="pluginName"
      :skill-name="skillName"
      @revert="revert"
    />

    <!-- Move-to-another-plugin modal (edit only) -->
    <Teleport to="body">
      <Transition name="confirm">
        <div
          v-if="moveOpen"
          class="se-move-backdrop"
          role="dialog"
          aria-modal="true"
          aria-labelledby="se-move-title"
          @mousedown.self="closeMove"
        >
          <div class="se-move">
            <h3 id="se-move-title" class="se-move__title">Move skill to another plugin</h3>
            <p class="se-move__path">
              <code>{{ pluginName }}/{{ skillName }}</code>
            </p>

            <div class="se-move__warn">
              <span class="se-move__warn-badge">heads up</span>
              <p class="se-move__warn-text">
                This changes where the skill is published. Anyone whose client
                installs it at <code>{{ pluginName }}/{{ skillName }}</code> will
                <strong>break</strong> until they re-point to its new plugin.
                The skill's files and version history move with it.
              </p>
            </div>

            <label class="se-move__label" for="se-move-target">destination plugin</label>
            <select
              id="se-move-target"
              v-model="moveTarget"
              class="se-move__select"
              :disabled="moving"
            >
              <option value="" disabled>select a plugin…</option>
              <option
                v-for="p in moveCandidates"
                :key="p.id"
                :value="p.name"
              >{{ p.name }}</option>
            </select>
            <p v-if="!moveCandidates.length" class="se-move__empty">
              no other plugins available — create another plugin first.
            </p>

            <ErrorAlert :message="moveError" />

            <div class="se-move__actions">
              <button
                type="button"
                class="se-btn"
                :disabled="moving"
                @click="closeMove"
              >cancel</button>
              <button
                type="button"
                class="se-btn se-btn--danger"
                :disabled="moving || !moveTarget"
                @click="confirmMove"
              >{{ moving ? 'moving…' : 'move skill' }}</button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.se {
  /* Tight container — reset the spacious global feel. */
  margin-top: -16px;
}

/* ─── Action bar ───────────────────────────────────────────────── */
.se-bar {
  position: sticky;
  top: 0;
  z-index: 20;
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
.se-bar__id {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1 1 auto;
}
.se-bar__kind {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  color: var(--accent);
  padding: 3px 8px;
  border: 1px solid var(--accent);
  background: transparent;
}
.se-bar__divider {
  width: 1px;
  height: 16px;
  background: var(--border);
}
.se-bar__path {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--text-soft);
  background: transparent;
  border: 0;
  padding: 0;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.se-bar__leaf { color: var(--text); }
.se-bar__state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--accent-2);
}
.se-bar__dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 0 0 rgb(var(--accent-rgb) / 0.55);
  animation: se-pulse 2.2s infinite;
}
@keyframes se-pulse {
  0%   { box-shadow: 0 0 0 0 rgb(var(--accent-rgb) / 0.5); }
  70%  { box-shadow: 0 0 0 8px rgb(var(--accent-rgb) / 0); }
  100% { box-shadow: 0 0 0 0 rgb(var(--accent-rgb) / 0); }
}
.se-bar__actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

/* ─── Flat button system (overrides global animated buttons) ───── */
.se-btn {
  /* fully neutralize the global animated button */
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
  transition: border-color 0.12s ease, color 0.12s ease, background 0.12s ease;
}
.se-btn::before { display: none; content: none; }
.se-btn:hover {
  background: transparent;
  color: var(--accent);
  border-color: var(--accent);
  transform: none;
}
.se-btn:active { transform: none; }
.se-btn:disabled,
.se-btn:disabled:hover {
  opacity: 0.35;
  cursor: not-allowed;
  color: var(--text-soft);
  border-color: var(--border);
}

.se-btn--primary {
  color: var(--text);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 700;
}
.se-btn--primary:hover {
  color: var(--bg);
  background: var(--text);
  border-color: var(--text);
}

.se-btn--danger {
  color: var(--rust);
  border-color: rgb(var(--rust-rgb) / 0.5);
}
.se-btn--danger:hover {
  color: var(--text);
  background: var(--rust);
  border-color: var(--rust);
}

.se-btn--ghost {
  border-color: transparent;
  color: var(--text-soft);
}
.se-btn--ghost:hover {
  color: var(--accent);
  border-color: var(--accent);
  background: transparent;
}

.se-btn--applied,
.se-btn--applied:hover {
  color: var(--bg);
  background: var(--success);
  border-color: var(--success);
  font-weight: 700;
}

.se-btn--unlock {
  color: var(--accent);
  border-color: rgb(var(--accent-rgb) / 0.5);
}
.se-btn--unlock:hover {
  color: var(--bg);
  background: var(--accent);
  border-color: var(--accent);
}

/* ─── Locked banner ────────────────────────────────────────────── */
.se-lock {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-top: 16px;
  padding: 12px 14px;
  border-left: 2px solid var(--rust);
  background: rgb(var(--rust-rgb) / 0.06);
}
.se-lock__badge {
  flex: 0 0 auto;
  align-self: flex-start;
  font-family: var(--mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--rust);
  border: 1px solid rgb(var(--rust-rgb) / 0.5);
  padding: 3px 8px;
  white-space: nowrap;
}
.se-lock__body { min-width: 0; }
.se-lock__title {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--text);
}
.se-lock__reason {
  margin: 6px 0 0;
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-soft);
}
.se-lock__hint {
  margin: 6px 0 0;
  font-size: 11.5px;
  color: var(--muted);
}

/* ─── Import notice (new mode) ─────────────────────────────────── */
.se-notice {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 14px;
  margin-top: 16px;
  border-left: 2px solid var(--accent);
  background: rgb(var(--accent-rgb) / 0.04);
}
.se-notice__text {
  flex: 1 1 240px;
  font-size: 12.5px;
  color: var(--text-soft);
  line-height: 1.55;
}

/* ─── Tabs ─────────────────────────────────────────────────────── */
.se-tabs {
  display: flex;
  gap: 0;
  margin: 20px 0 0;
  border-bottom: 1px solid var(--border);
}
.se-tab {
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  padding: 10px 16px;
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
.se-tab::before { display: none; content: none; }
.se-tab:hover { color: var(--text); transform: none; background: transparent; }
.se-tab--active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.se-tab__count {
  font-size: 10.5px;
  color: var(--muted);
  letter-spacing: 0;
}

/* ─── Form ─────────────────────────────────────────────────────── */
.se-form {
  padding-top: 8px;
}
.se-field {
  display: block;
  margin-top: 22px;
}
.se-field__label {
  display: block;
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.se-field__label-tag {
  font-weight: 400;
  letter-spacing: 0.06em;
  text-transform: none;
  color: var(--muted);
  margin-left: 6px;
}
.se-field__input {
  width: 100%;
  background: var(--bg-2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 9px 12px;
  font-family: var(--mono);
  font-size: 13.5px;
  outline: none;
  transition: border-color 0.15s ease;
}
.se-field__input:focus {
  border-color: var(--accent);
}
.se-field__input::placeholder { color: var(--muted); }
.se-field__readonly {
  font-family: var(--mono);
  font-size: 14px;
  color: var(--text);
  padding: 8px 12px;
  background: var(--bg-2);
  border: 1px dashed var(--border);
}
.se-field__hint {
  margin: 6px 0 0;
  font-size: 11.5px;
  color: var(--muted);
  letter-spacing: 0.02em;
  line-height: 1.55;
}
.se-field__input--invalid {
  border-color: var(--rust);
}
.se-field__input--invalid:focus {
  border-color: var(--rust);
}
.se-field__error {
  margin: 6px 0 0;
  font-size: 11.5px;
  color: var(--rust);
  letter-spacing: 0.02em;
  line-height: 1.55;
}
.se-field__textarea {
  width: 100%;
  background: var(--bg-2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 10px 12px;
  margin: 0;
  font-family: var(--mono);
  font-size: 13px;
  line-height: 1.55;
  outline: none;
  resize: vertical;
  min-height: 0;
  transition: border-color 0.15s ease;
}
.se-field__textarea:focus { border-color: var(--accent); }
.se-field__textarea--code {
  font-size: 12.5px;
  white-space: pre;
}
.se-field__editor {
  /* Wrapper around MarkdownEditor — no styles needed, editor brings its own. */
}

/* Collapse panel for extra frontmatter — matches audit/history bar */
.se-field--collapse {
  margin-top: 22px;
  padding: 0;
  border: 0;
  background: transparent;
}
.se-field__summary {
  list-style: none;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  background: var(--bg-2);
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
  user-select: none;
}
.se-field__summary::-webkit-details-marker { display: none; }
.se-field__toggle {
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
.se-field--collapse:not([open]) > .se-field__summary .se-field__toggle::before { content: '+'; }
.se-field--collapse[open] > .se-field__summary .se-field__toggle::before { content: '−'; }
.se-field__summary-label { letter-spacing: inherit; flex: 0 0 auto; }
.se-field__summary-tag {
  font-weight: 400;
  letter-spacing: 0.06em;
  text-transform: none;
  color: var(--muted);
  font-size: 11px;
  flex: 0 0 auto;
}
.se-field__summary-hint {
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
.se-field__summary-hint-open,
.se-field__summary-hint-close { display: none; }
.se-field--collapse:not([open]) > .se-field__summary .se-field__summary-hint-open { display: inline; }
.se-field--collapse[open] > .se-field__summary .se-field__summary-hint-close { display: inline; }
.se-field__summary-chev {
  display: inline-block;
  color: var(--accent);
  font-size: 12px;
  transition: transform 0.18s ease;
  letter-spacing: 0;
}
.se-field--collapse[open] > .se-field__summary .se-field__summary-chev { transform: rotate(90deg); }
.se-field__summary:hover {
  color: var(--text);
  border-color: var(--accent);
  background: rgb(var(--accent-rgb) / 0.04);
}
.se-field__summary:hover .se-field__toggle { border-color: var(--accent); }
.se-field__summary:hover .se-field__summary-hint { color: var(--text-soft); }
.se-field--collapse[open] > .se-field__summary {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.se-field__collapse-body {
  padding: 12px 14px 4px;
  border-left: 1px solid var(--border);
  border-right: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}

.se-form__foot {
  margin: 22px 0 0;
  padding: 10px 12px;
  font-size: 11.5px;
  color: var(--muted);
  border-left: 2px solid var(--border);
  background: var(--bg-2);
}

/* ─── Files tab (split pane) ───────────────────────────────────── */
.se-files {
  display: grid;
  grid-template-columns: minmax(260px, 300px) 1fr;
  gap: 0;
  margin-top: 20px;
  border: 1px solid var(--border);
  background: var(--bg-2);
  min-height: 480px;
}
@media (max-width: 880px) {
  .se-files { grid-template-columns: 1fr; }
}

.se-tree {
  border-right: 1px solid var(--border);
  padding: 14px 16px;
  background: var(--bg);
}
@media (max-width: 880px) {
  .se-tree { border-right: 0; border-bottom: 1px solid var(--border); }
}
.se-tree__intro {
  margin: 0 0 14px;
  font-size: 11.5px;
  color: var(--muted);
  line-height: 1.55;
}

.se-tree__group {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid var(--border-soft);
}
.se-tree__group:first-of-type {
  margin-top: 0;
  padding-top: 0;
  border-top: 0;
}
.se-tree__head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}
.se-tree__name {
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--text);
}
.se-tree__count {
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--muted);
}
.se-tree__act {
  background: transparent;
  border: 0;
  color: var(--text-soft);
  padding: 2px 6px;
  margin: 0;
  font-family: var(--mono);
  font-size: 10.5px;
  letter-spacing: 0.04em;
  text-transform: none;
  cursor: pointer;
  font-weight: 500;
  transition: color 0.12s ease;
}
.se-tree__act::before { display: none; content: none; }
.se-tree__act:hover { color: var(--accent); background: transparent; transform: none; }
.se-tree__hint {
  margin: 0 0 6px;
  font-size: 10.5px;
  color: var(--muted);
  line-height: 1.5;
}
.se-tree__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.se-tree__item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-radius: 0;
  padding: 5px 8px;
  margin: 0;
  font-family: var(--mono);
  font-size: 12px;
  letter-spacing: 0;
  text-transform: none;
  font-weight: 500;
  cursor: pointer;
  text-align: left;
  transition: color 0.1s ease, background 0.1s ease;
}
.se-tree__item::before { display: none; content: none; }
.se-tree__item:hover {
  color: var(--text);
  background: rgb(var(--text-rgb) / 0.05);
  transform: none;
}
.se-tree__item--active,
.se-tree__item--active:hover {
  color: var(--text);
  background: var(--accent);
}
.se-tree__item--active .se-tree__item-meta,
.se-tree__item--active .se-tree__chev {
  color: var(--text);
}
.se-tree__chev {
  flex: 0 0 auto;
  color: var(--muted);
  width: 10px;
  text-align: center;
}
.se-tree__item-name {
  flex: 1;
  min-width: 0;
  word-break: break-all;
}
.se-tree__item-meta {
  font-size: 10px;
  color: var(--muted);
  flex: 0 0 auto;
  letter-spacing: 0;
}
.se-tree__empty {
  padding: 4px 8px 4px 18px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-style: normal;
  color: var(--muted);
}

/* ─── Editor pane ──────────────────────────────────────────────── */
.se-pane {
  display: flex;
  flex-direction: column;
  padding: 14px 16px;
  min-width: 0;
}
.se-pane__empty {
  margin: auto;
  padding: 40px 16px;
  text-align: center;
}
.se-pane__empty-title {
  font-family: var(--mono);
  font-size: 13px;
  font-weight: 700;
  color: var(--text);
  margin: 0 0 6px;
  letter-spacing: 0.04em;
}
.se-pane__empty-hint {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.se-pane__head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border-soft);
  margin-bottom: 12px;
}
.se-pane__path {
  font-family: var(--mono);
  font-size: 13px;
  background: transparent;
  border: 0;
  padding: 0;
  color: var(--text);
}
.se-pane__meta {
  font-family: var(--mono);
  font-size: 10.5px;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--muted);
}
.se-pane__loading {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.se-pane__editor {
  width: 100%;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 12px 14px;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.6;
  outline: none;
  resize: vertical;
  min-height: 360px;
}
.se-pane__editor:focus { border-color: var(--accent); }
.se-pane__binary {
  margin: 0;
  padding: 14px;
  font-size: 12px;
  color: var(--muted);
  background: var(--bg);
  border: 1px dashed var(--border);
}
.se-pane__actions {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}

/* ─── Section (review) ─────────────────────────────────────────── */
.se-section {
  margin-top: 28px;
  padding: 16px 0 0;
  border-top: 1px solid var(--border);
}
.se-section__head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.se-section__title {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.28em;
  text-transform: uppercase;
  color: var(--text);
}
.se-progress {
  margin: 4px 0 4px;
}
.se-progress__bar {
  position: relative;
  height: 3px;
  background: var(--bg-2);
  border: 1px solid var(--border);
  overflow: hidden;
}
.se-progress__fill {
  position: absolute;
  top: 0;
  bottom: 0;
  left: -40%;
  width: 40%;
  background: linear-gradient(
    90deg,
    transparent 0%,
    var(--accent) 50%,
    transparent 100%
  );
  animation: se-progress-slide 1.4s ease-in-out infinite;
}
@keyframes se-progress-slide {
  0%   { left: -40%; }
  100% { left: 100%; }
}
.se-progress__label {
  margin: 8px 0 0;
  font-family: var(--mono);
  font-size: 12px;
  letter-spacing: 0.02em;
  color: var(--text-soft);
}
.se-progress__dots::after {
  content: '';
  animation: se-progress-dots 1.4s steps(4, end) infinite;
}
@keyframes se-progress-dots {
  0%   { content: ''; }
  25%  { content: '.'; }
  50%  { content: '..'; }
  75%  { content: '...'; }
  100% { content: ''; }
}

.se-review__summary {
  font-family: var(--mono);
  font-size: 13.5px;
  line-height: 1.55;
  color: var(--text);
  margin: 4px 0 12px;
}
.se-review__counts {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  margin-bottom: 14px;
}
.se-review__pass {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--success);
  letter-spacing: 0.04em;
}

.se-tag {
  display: inline-flex;
  align-items: center;
  padding: 2px 9px;
  border-radius: 0;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.16em;
  text-transform: lowercase;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text-soft);
  white-space: nowrap;
}
.se-tag--problem {
  color: var(--rust);
  border-color: var(--rust);
}
.se-tag--warning {
  color: var(--accent-2);
  border-color: var(--accent);
}
.se-tag--info {
  color: var(--text-soft);
  border-color: var(--border);
}

.se-findings {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.se-finding {
  border: 1px solid var(--border);
  border-left-width: 3px;
  background: var(--bg-2);
  padding: 10px 12px;
}
.se-finding--problem { border-left-color: var(--rust); }
.se-finding--warning { border-left-color: var(--accent); }
.se-finding--info    { border-left-color: var(--border); }
.se-finding__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}
.se-finding__sev {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: lowercase;
  color: var(--muted);
}
.se-finding--problem .se-finding__sev { color: var(--rust); }
.se-finding--warning .se-finding__sev { color: var(--accent-2); }
.se-finding__title {
  font-family: var(--mono);
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.se-finding__detail {
  margin: 6px 0 0;
  color: var(--text-soft);
  font-size: 12.5px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}
.se-finding__apply {
  font-size: 10.5px;
  padding: 3px 9px;
  letter-spacing: 0.06em;
  flex: 0 0 auto;
}
.se-finding__note {
  margin: 6px 0 0;
  font-family: var(--mono);
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--success);
}
.se-finding__error {
  margin: 6px 0 0;
  font-family: var(--mono);
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--rust);
}
.se-btn--loading {
  color: var(--text-soft);
  border-color: var(--accent);
}

.se-suggest {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  margin-top: 14px;
  padding: 12px 14px;
  border: 1px dashed var(--accent);
  background: rgb(var(--accent-rgb) / 0.04);
}
.se-suggest__body { flex: 1; min-width: 0; }
.se-suggest__label {
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--accent-2);
  margin-bottom: 4px;
}
.se-suggest__text {
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--text);
}

/* ─── Disclosure (audit) ───────────────────────────────────────── */
.se-disclosure {
  margin-top: 22px;
}
.se-disclosure__head {
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
.se-disclosure__head::-webkit-details-marker { display: none; }
.se-disclosure__toggle {
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
  transition: border-color 0.15s ease, background 0.15s ease, color 0.15s ease;
}
.se-disclosure[open] > .se-disclosure__head .se-disclosure__toggle::before {
  content: '−';
}
.se-disclosure:not([open]) > .se-disclosure__head .se-disclosure__toggle::before {
  content: '+';
}
.se-disclosure__title { letter-spacing: inherit; flex: 0 0 auto; }
.se-disclosure__hint {
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
.se-disclosure__hint-open,
.se-disclosure__hint-close { display: none; }
.se-disclosure:not([open]) > .se-disclosure__head .se-disclosure__hint-open { display: inline; }
.se-disclosure[open] > .se-disclosure__head .se-disclosure__hint-close { display: inline; }
.se-disclosure__chev {
  display: inline-block;
  color: var(--accent);
  font-size: 12px;
  transition: transform 0.18s ease;
  letter-spacing: 0;
}
.se-disclosure[open] > .se-disclosure__head .se-disclosure__chev { transform: rotate(90deg); }
.se-disclosure__head:hover {
  color: var(--text);
  border-color: var(--accent);
  background: rgb(var(--accent-rgb) / 0.04);
}
.se-disclosure__head:hover .se-disclosure__toggle {
  border-color: var(--accent);
}
.se-disclosure__head:hover .se-disclosure__hint {
  color: var(--text-soft);
}
.se-disclosure[open] > .se-disclosure__head {
  color: var(--text);
  border-bottom-color: var(--accent);
}

.se-audit {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 6px 16px;
  margin: 8px 0 0;
  font-family: var(--mono);
  font-size: 12.5px;
}
.se-audit dt {
  color: var(--muted);
  font-size: 10.5px;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  padding-top: 2px;
}
.se-audit dd {
  margin: 0;
  color: var(--text);
}
.se-audit__dim { color: var(--muted); }

/* Small screens */
@media (max-width: 720px) {
  .se-bar { padding: 12px; gap: 10px; }
  .se-bar__actions { gap: 4px; }
  .se-btn { padding: 5px 10px; font-size: 11px; }
}

/* ─── Move modal ───────────────────────────────────────────────── */
.se-move-backdrop {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: grid;
  place-items: center;
  padding: 16px;
  background: rgb(0 0 0 / 0.55);
}
.se-move {
  width: 100%;
  max-width: 460px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-top: 2px solid var(--accent);
  padding: 20px;
}
.se-move__title {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 14px;
  font-weight: 700;
  letter-spacing: 0.04em;
  color: var(--text);
}
.se-move__path {
  margin: 0 0 16px;
  font-size: 12.5px;
}
.se-move__path code {
  font-family: var(--mono);
  color: var(--text-soft);
}
.se-move__warn {
  display: flex;
  gap: 10px;
  padding: 12px;
  margin-bottom: 18px;
  border-left: 2px solid var(--rust);
  background: rgb(var(--rust-rgb) / 0.06);
}
.se-move__warn-badge {
  flex: 0 0 auto;
  align-self: flex-start;
  font-family: var(--mono);
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--rust);
  border: 1px solid rgb(var(--rust-rgb) / 0.5);
  padding: 2px 6px;
}
.se-move__warn-text {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.55;
  color: var(--text-soft);
}
.se-move__warn-text code {
  font-family: var(--mono);
  color: var(--text);
}
.se-move__warn-text strong { color: var(--rust); }
.se-move__label {
  display: block;
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.se-move__select {
  width: 100%;
  background: var(--bg-2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: 9px 12px;
  font-family: var(--mono);
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s ease;
}
.se-move__select:focus { border-color: var(--accent); }
.se-move__empty {
  margin: 8px 0 0;
  font-size: 11.5px;
  color: var(--muted);
}
.se-move__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 18px;
}

/* Reuse the global "confirm" transition timing for the move modal. */
.confirm-enter-active,
.confirm-leave-active { transition: opacity 0.16s ease; }
.confirm-enter-from,
.confirm-leave-to { opacity: 0; }
.confirm-enter-active .se-move,
.confirm-leave-active .se-move { transition: transform 0.16s ease; }
.confirm-enter-from .se-move,
.confirm-leave-to .se-move { transform: translateY(8px); }
</style>
