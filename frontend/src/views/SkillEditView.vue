<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch, nextTick } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { api, errMsg } from '../api'
import type { ValidationReport, FindingSeverity, Finding } from '../types'
import { useConfirm } from '../composables/useConfirm'
import {
  useSkillFileManager,
  fmtBytes,
  FOLDER_HINT,
  isWellKnownFolder,
} from '../composables/useSkillFileManager'
import SkillVersionHistory from '../components/SkillVersionHistory.vue'
import ErrorAlert from '../components/ErrorAlert.vue'
import MarkdownEditor from '../components/MarkdownEditor.vue'
import { usePluginStore } from '../stores/plugins'

const pluginStore = usePluginStore()

const { confirm } = useConfirm()

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

onBeforeRouteLeave(async () => {
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
})

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
// (e.g. just after import) reuses the instance and skips onMounted — reload
// explicitly when the target skill name changes.
watch(() => props.skillName, load)
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
          v-if="isEdit"
          type="button"
          class="se-btn se-btn--danger"
          @click="deleteSkill"
        >delete</button>
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
          :disabled="loading || importing"
          @click="submit"
        >{{ loading ? 'saving…' : (isEdit ? 'save' : 'create') }}</button>
      </div>
    </header>

    <!-- ZIP import bar (new mode only) -->
    <div v-if="!isEdit" class="se-notice">
      <input
        ref="importInput"
        type="file"
        accept=".zip,application/zip"
        hidden
        @change="onImportFile"
      />
      <span class="se-notice__text">
        already packaged as a ZIP? imports <code>SKILL.md</code>, <code>scripts/</code>, <code>references/</code>, <code>assets/</code> in one go.
      </span>
      <button
        type="button"
        class="se-btn se-btn--ghost"
        :disabled="importing"
        @click="triggerImport"
      >{{ importing ? 'importing…' : 'import zip ↗' }}</button>
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
          pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]"
          placeholder="my-skill-slug"
          class="se-field__input"
        />
        <p v-if="!isEdit" class="se-field__hint">lowercase letters, digits, hyphens · used as the skill directory name</p>
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
            <span class="se-tree__name">{{ folder }}/</span>
            <span class="se-tree__count">[{{ filesByFolder[folder]?.length ?? 0 }}]</span>
            <span class="spacer"></span>
            <button
              v-if="isWellKnownFolder(folder)"
              type="button"
              class="se-tree__act"
              title="New file"
              @click="promptNewFile(folder)"
            >+ new</button>
            <button
              type="button"
              class="se-tree__act"
              title="Upload files"
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
                <span class="se-tree__item-name">{{ f.path.slice(folder.length + 1) }}</span>
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
            <button v-if="!fileLoading" type="button" class="se-btn se-btn--danger" @click="deleteCurrentFile">delete</button>
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
              binary file — cannot edit inline. download or upload a replacement onto <code>{{ selectedPath.split('/')[0] }}/</code>.
            </p>

            <div v-if="!fileIsBinary" class="se-pane__actions">
              <button
                type="button"
                class="se-btn se-btn--primary"
                :disabled="!fileDirty"
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
  box-shadow: 0 0 0 0 rgba(245, 165, 36, 0.55);
  animation: se-pulse 2.2s infinite;
}
@keyframes se-pulse {
  0%   { box-shadow: 0 0 0 0 rgba(245, 165, 36, 0.5); }
  70%  { box-shadow: 0 0 0 8px rgba(245, 165, 36, 0); }
  100% { box-shadow: 0 0 0 0 rgba(245, 165, 36, 0); }
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
  color: var(--bg);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 700;
}
.se-btn--primary:hover {
  color: var(--bg);
  background: var(--accent-2);
  border-color: var(--accent-2);
}

.se-btn--danger {
  color: var(--rust);
  border-color: rgba(214, 90, 49, 0.5);
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

/* ─── Import notice (new mode) ─────────────────────────────────── */
.se-notice {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 14px;
  margin-top: 16px;
  border-left: 2px solid var(--accent);
  background: rgba(245, 165, 36, 0.04);
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
  background: rgba(245, 165, 36, 0.04);
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
  background: rgba(255, 255, 255, 0.025);
  transform: none;
}
.se-tree__item--active,
.se-tree__item--active:hover {
  color: var(--bg);
  background: var(--accent);
}
.se-tree__item--active .se-tree__item-meta,
.se-tree__item--active .se-tree__chev {
  color: var(--bg);
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
  background: rgba(245, 165, 36, 0.04);
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
  background: rgba(245, 165, 36, 0.04);
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
</style>
