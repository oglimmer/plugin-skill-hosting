<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { api, errMsg } from '../api'
import type { ValidationReport, FindingSeverity } from '../types'
import { useConfirm } from '../composables/useConfirm'
import {
  useSkillFileManager,
  fmtBytes,
  FOLDER_ORDER,
  FOLDER_HINT,
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
  scriptsInput,
  referencesInput,
  assetsInput,
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
const SEVERITY_LABEL: Record<FindingSeverity, string> = {
  problem: 'Problem',
  warning: 'Warning',
  info: 'Info',
}

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

async function validate() {
  validationError.value = ''
  validationReport.value = null
  validating.value = true
  try {
    validationReport.value = await api.validateSkill({
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
onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))
// Same component backs /skills/new and /skills/:name/edit, so a route change
// (e.g. just after import) reuses the instance and skips onMounted — reload
// explicitly when the target skill name changes.
watch(() => props.skillName, load)
</script>

<template>
  <h1>{{ isEdit ? `Edit skill: ${skillName}` : 'New skill' }}</h1>

  <!-- Create mode: simple single-column form (no file tree until skill exists) -->
  <div v-if="!isEdit" class="card">
    <input
      ref="importInput"
      type="file"
      accept=".zip,application/zip"
      style="display: none"
      @change="onImportFile"
    />
    <div class="row" style="margin-bottom: 16px; gap: 8px; flex-wrap: wrap; align-items: center">
      <p class="muted" style="margin: 0; flex: 1; min-width: 240px">
        Have a skill already packaged as a ZIP? Import its <code>SKILL.md</code> and
        <code>scripts/</code>, <code>references/</code>, <code>assets/</code> folders in one go.
      </p>
      <button
        type="button"
        class="secondary"
        :disabled="importing"
        @click="triggerImport"
      >
        {{ importing ? 'Importing…' : 'Import from ZIP' }}
      </button>
    </div>

    <form @submit.prevent="submit">
      <label>Skill name (slug, lowercase, [a-z0-9-])</label>
      <input
        v-model="name"
        required
        pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]"
      />

      <label>Description (used by Claude to decide when to use this skill)</label>
      <textarea
        v-model="description"
        required
        rows="6"
        class="description-textarea"
        @input="markTouched"
      />

      <details class="extra-frontmatter" :open="!!extraFrontmatter">
        <summary>Extra YAML frontmatter (advanced)</summary>
        <p class="muted extra-frontmatter-hint">
          Optional YAML lines emitted into <code>SKILL.md</code> between
          <code>name/description</code> and the closing <code>---</code>.
          Use this for keys like <code>allowed-tools</code>, <code>license</code>,
          or other metadata Claude Code recognises.
        </p>
        <textarea
          v-model="extraFrontmatter"
          rows="4"
          class="extra-frontmatter-textarea"
          spellcheck="false"
          placeholder="allowed-tools:&#10;  - Read&#10;  - Edit"
          @input="markTouched"
        />
      </details>

      <label>Body (Markdown — becomes the contents of SKILL.md after the frontmatter)</label>
      <div @input="markTouched" @keydown="markTouched">
        <MarkdownEditor v-model="body" />
      </div>

      <ErrorAlert :message="error" />
      <div class="row" style="margin-top: 16px; gap: 8px; flex-wrap: wrap">
        <button type="submit" :disabled="loading || importing">
          {{ loading ? 'Saving…' : 'Create skill' }}
        </button>
        <button
          type="button"
          class="secondary"
          :disabled="validating || (!description && !body)"
          @click="validate"
        >
          {{ validating ? 'Validating…' : 'Validate' }}
        </button>
        <button type="button" class="secondary" @click="cancel">Cancel</button>
      </div>
      <p class="muted" style="margin-top: 12px">
        Supporting files (scripts/, references/, assets/) can be added after the skill is created.
      </p>
    </form>
  </div>

  <!-- Edit mode: tabs -->
  <div v-else>
    <input
      ref="scriptsInput"
      type="file"
      multiple
      style="display: none"
      @change="onUploadChange('scripts', $event)"
    />
    <input
      ref="referencesInput"
      type="file"
      multiple
      style="display: none"
      @change="onUploadChange('references', $event)"
    />
    <input
      ref="assetsInput"
      type="file"
      multiple
      style="display: none"
      @change="onUploadChange('assets', $event)"
    />
    <nav class="tabs">
      <button
        type="button"
        class="tab"
        :class="{ 'tab--active': tab === 'skill' }"
        @click="tab = 'skill'"
      >
        SKILL
        <span class="tab-hint">SKILL.md</span>
      </button>
      <button
        type="button"
        class="tab"
        :class="{ 'tab--active': tab === 'more' }"
        @click="tab = 'more'"
      >
        MORE
        <span class="tab-hint">
          {{ files.length === 0 ? 'scripts · references · assets' : `${files.length} file${files.length === 1 ? '' : 's'}` }}
        </span>
      </button>
    </nav>

    <!-- SKILL tab: simple description + body editor -->
    <div v-if="tab === 'skill'" class="card">
      <form @submit.prevent="submit">
        <label>Skill name (slug, lowercase, [a-z0-9-])</label>
        <input
          v-model="name"
          disabled
          pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]"
        />

        <label>Description (used by Claude to decide when to use this skill)</label>
        <textarea
          v-model="description"
          required
          rows="6"
          class="description-textarea"
          @input="markTouched"
        />

        <details class="extra-frontmatter" :open="!!extraFrontmatter">
          <summary>Extra YAML frontmatter (advanced)</summary>
          <p class="muted extra-frontmatter-hint">
            Optional YAML lines emitted into <code>SKILL.md</code> between
            <code>name/description</code> and the closing <code>---</code>.
            Imported skills preserve these verbatim.
          </p>
          <textarea
            v-model="extraFrontmatter"
            rows="4"
            class="extra-frontmatter-textarea"
            spellcheck="false"
            placeholder="allowed-tools:&#10;  - Read&#10;  - Edit"
            @input="markTouched"
          />
        </details>

        <label>Body (Markdown — becomes the contents of SKILL.md after the frontmatter)</label>
        <div @input="markTouched" @keydown="markTouched">
          <MarkdownEditor v-model="body" />
        </div>

        <ErrorAlert :message="error" />
        <div class="row" style="margin-top: 16px; gap: 8px; flex-wrap: wrap">
          <button type="submit" :disabled="loading">
            {{ loading ? 'Saving…' : 'Save' }}
          </button>
          <button
            type="button"
            class="secondary"
            :disabled="validating || (!description && !body)"
            @click="validate"
          >
            {{ validating ? 'Validating…' : 'Validate' }}
          </button>
          <button type="button" class="secondary" @click="cancel">Cancel</button>
        </div>
      </form>
    </div>

    <!-- MORE tab: file tree + per-file editor -->
    <div v-else class="skill-editor">
      <aside class="file-tree card">
        <p class="muted" style="margin-top: 0">
          Optional supporting files Claude can use alongside SKILL.md. Most skills don't need any.
        </p>
        <ErrorAlert v-if="!selectedPath && fileError" :message="fileError" />
        <div v-for="folder in FOLDER_ORDER" :key="folder" class="tree-folder"
             @dragover.prevent
             @drop="onDrop(folder, $event)">
          <header class="tree-folder-header">
            <span class="tree-folder-name">{{ folder }}/</span>
            <span class="spacer" />
            <button
              type="button"
              class="iconbtn"
              title="New file"
              @click="promptNewFile(folder)"
            >+ new</button>
            <button
              type="button"
              class="iconbtn"
              title="Upload files"
              @click="triggerUpload(folder)"
            >↑ upload</button>
          </header>
          <p class="tree-folder-hint muted">{{ FOLDER_HINT[folder] }}</p>
          <ul class="tree-list">
            <li v-for="f in filesByFolder[folder]" :key="f.path">
              <button
                type="button"
                class="tree-item tree-item--file"
                :class="{ 'tree-item--active': selectedPath === f.path }"
                @click="selectFile(f.path)"
              >
                <span class="tree-item-name">{{ f.path.slice(folder.length + 1) }}</span>
                <span class="tree-item-meta muted">
                  {{ f.isBinary ? 'bin' : 'txt' }} · {{ fmtBytes(f.sizeBytes) }}
                </span>
              </button>
            </li>
            <li v-if="filesByFolder[folder].length === 0" class="tree-empty muted">empty</li>
          </ul>
        </div>
      </aside>

      <section class="editor-pane">
        <div v-if="selectedPath === null" class="card empty-state">
          <h2 style="margin-top: 0">No file selected</h2>
          <p class="muted">
            Pick a file on the left, or use <code>+ new</code> / <code>↑ upload</code>
            to add one. You can also drag-drop files onto a folder header.
          </p>
        </div>

        <div v-else class="card">
          <div class="row" style="align-items: baseline; gap: 12px; flex-wrap: wrap">
            <h2 style="margin: 0">{{ selectedPath }}</h2>
            <span class="badge">{{ fileIsBinary ? 'binary' : 'text' }}</span>
            <span class="muted">{{ fmtBytes(fileSize) }}</span>
            <span class="spacer" />
            <button
              v-if="!fileLoading"
              type="button"
              class="secondary"
              @click="downloadCurrentFile"
            >Download</button>
            <button
              v-if="!fileLoading"
              type="button"
              class="danger"
              @click="deleteCurrentFile"
            >Delete</button>
          </div>

          <p v-if="fileLoading" class="muted">Loading…</p>
          <ErrorAlert v-else-if="fileError" :message="fileError" />

          <template v-else>
            <textarea
              v-if="!fileIsBinary"
              v-model="fileContent"
              @input="fileDirty = true"
              style="margin-top: 14px; min-height: 360px"
            />
            <p v-else class="muted" style="margin-top: 14px">
              Binary content cannot be edited inline. Use Download to fetch it,
              or upload a new version by dragging a replacement onto the
              <code>{{ selectedPath.split('/')[0] }}/</code> folder.
            </p>

            <div class="row" style="margin-top: 14px; gap: 8px">
              <button
                v-if="!fileIsBinary"
                type="button"
                :disabled="!fileDirty"
                @click="saveCurrentFile"
              >Save</button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </div>

  <div v-if="validating || validationError || validationReport" class="card review">
    <h2 style="margin-top: 0">Claude review</h2>
    <p v-if="validating" class="muted">Asking Claude to review the skill…</p>
    <ErrorAlert :message="validationError" />

    <template v-if="validationReport">
      <p v-if="validationReport.summary" class="review-summary">
        {{ validationReport.summary }}
      </p>

      <div class="review-counts">
        <span class="finding-chip problem" v-if="findingCounts.problem">
          {{ findingCounts.problem }} Problem<span v-if="findingCounts.problem !== 1">s</span>
        </span>
        <span class="finding-chip warning" v-if="findingCounts.warning">
          {{ findingCounts.warning }} Warning<span v-if="findingCounts.warning !== 1">s</span>
        </span>
        <span class="finding-chip info" v-if="findingCounts.info">
          {{ findingCounts.info }} Info
        </span>
        <span v-if="!sortedFindings.length" class="muted">
          No issues found — looks good.
        </span>
      </div>

      <ul v-if="sortedFindings.length" class="findings">
        <li
          v-for="(f, i) in sortedFindings"
          :key="`${f.severity}:${f.title}:${i}`"
          class="finding"
          :class="`finding--${f.severity}`"
        >
          <div class="finding-head">
            <span class="finding-chip" :class="f.severity">{{ SEVERITY_LABEL[f.severity] }}</span>
            <span class="finding-title">{{ f.title }}</span>
          </div>
          <p class="finding-detail">{{ f.detail }}</p>
        </li>
      </ul>

      <div v-if="validationReport.suggestedDescription" class="suggested-desc">
        <div class="row" style="justify-content: space-between; align-items: flex-start; gap: 12px">
          <div>
            <div class="suggested-desc-label">Suggested description</div>
            <div class="suggested-desc-text">{{ validationReport.suggestedDescription }}</div>
          </div>
          <button type="button" class="secondary" @click="applySuggestedDescription">
            Apply
          </button>
        </div>
      </div>
    </template>
  </div>

  <details v-if="isEdit" class="card collapsible-card">
    <summary><h2>Audit</h2></summary>
    <table>
      <tbody>
        <tr>
          <th>Created</th>
          <td>{{ audit.createdByName || '—' }} · {{ fmt(audit.createdAt) }}</td>
        </tr>
        <tr>
          <th>Last edited</th>
          <td>{{ audit.updatedByName || '—' }} · {{ fmt(audit.updatedAt) }}</td>
        </tr>
      </tbody>
    </table>
  </details>

  <SkillVersionHistory
    v-if="isEdit"
    ref="versionHistory"
    :plugin-name="pluginName"
    :skill-name="skillName"
    @revert="revert"
  />
</template>

<style scoped>
.description-textarea {
  min-height: 0;
  resize: vertical;
}
.extra-frontmatter {
  margin: 0 0 16px;
  border: 1px solid var(--border-soft, var(--border));
  border-radius: 4px;
  padding: 8px 12px;
}
.extra-frontmatter > summary {
  cursor: pointer;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--text-soft);
}
.extra-frontmatter[open] > summary {
  color: var(--text);
  margin-bottom: 8px;
}
.extra-frontmatter-hint {
  margin: 4px 0 8px;
  font-size: 12px;
}
.extra-frontmatter-textarea {
  font-family: var(--mono);
  font-size: 12.5px;
  min-height: 0;
  resize: vertical;
  white-space: pre;
}
.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 24px;
  border-bottom: 1px solid var(--border);
}
.tab {
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  padding: 12px 20px;
  margin-bottom: -1px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  cursor: pointer;
  display: inline-flex;
  align-items: baseline;
  gap: 10px;
  transition: color 0.15s ease, border-color 0.15s ease;
}
.tab::before { display: none; }
.tab:hover {
  color: var(--text);
  transform: none;
}
.tab--active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.tab-hint {
  font-size: 10px;
  font-weight: 400;
  letter-spacing: 0.1em;
  text-transform: none;
  color: var(--muted);
}

.empty-state {
  text-align: center;
  padding: 48px 32px;
}
.empty-state h2 { margin-bottom: 8px; }

.skill-editor {
  display: grid;
  grid-template-columns: minmax(240px, 280px) 1fr;
  gap: 24px;
  margin-bottom: 24px;
  align-items: start;
}
@media (max-width: 880px) {
  .skill-editor {
    grid-template-columns: 1fr;
  }
}

.file-tree {
  position: sticky;
  top: 96px;
  padding: 22px 22px;
}
@media (max-width: 880px) {
  .file-tree { position: static; }
}

.tree-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  background: transparent;
  color: var(--text-soft);
  border: 0;
  border-left: 2px solid transparent;
  padding: 8px 10px;
  margin: 0;
  font-family: var(--mono);
  font-size: 12.5px;
  letter-spacing: 0;
  text-transform: none;
  font-weight: 500;
  cursor: pointer;
  text-align: left;
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
}
.tree-item::before { display: none; }
.tree-item:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.025);
  transform: none;
}
.tree-item--active {
  color: var(--text);
  border-left-color: var(--accent);
  background: rgba(245, 165, 36, 0.06);
}
.tree-item-name { flex: 1; min-width: 0; word-break: break-all; }
.tree-item-meta { font-size: 10.5px; flex: 0 0 auto; }
.tree-item-badge {
  font-size: 9.5px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--accent-2);
  border: 1px solid var(--border);
  padding: 1px 6px;
  border-radius: 999px;
  flex: 0 0 auto;
}

.tree-folder {
  margin-top: 18px;
  border-top: 1px solid var(--border-soft);
  padding-top: 12px;
}
.tree-folder-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 10px;
  margin-bottom: 4px;
}
.tree-folder-name {
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text);
}
.tree-folder-hint {
  font-size: 11px;
  margin: 0 10px 6px;
}
.iconbtn {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-soft);
  padding: 4px 8px;
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease;
}
.iconbtn::before { display: none; }
.iconbtn:hover {
  color: var(--accent);
  border-color: var(--accent);
  transform: none;
}

.tree-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.tree-empty {
  padding: 6px 12px;
  font-size: 11px;
  font-style: italic;
}

.editor-pane > .card { margin-bottom: 0; }

.collapsible-card > summary {
  cursor: pointer;
  list-style: none;
  display: flex;
  align-items: center;
  gap: 10px;
}
.collapsible-card > summary::-webkit-details-marker { display: none; }
.collapsible-card > summary::before {
  content: '▸';
  display: inline-block;
  font-size: 12px;
  color: var(--text-soft);
  transition: transform 0.15s ease;
}
.collapsible-card[open] > summary::before { transform: rotate(90deg); }
.collapsible-card > summary > h2 {
  margin: 0;
  display: inline;
}
.collapsible-card[open] > summary { margin-bottom: 16px; }
</style>
