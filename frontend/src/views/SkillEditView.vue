<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import {
  api,
  type SkillVersion,
  type ValidationReport,
  type FindingSeverity,
  type SkillFileSummary,
} from '../api'
import { useConfirm } from '../composables/useConfirm'

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
const error = ref('')
const loading = ref(false)
const versions = ref<SkillVersion[]>([])
const versionsError = ref('')
const validating = ref(false)
const validationReport = ref<ValidationReport | null>(null)
const validationError = ref('')

// Tabs: SKILL = description+body editor; MORE = supporting files. Most users
// only ever need SKILL, so we default there and keep the file tree out of
// sight until they opt in.
type Tab = 'skill' | 'more'
const tab = ref<Tab>('skill')

// File tree state ────────────────────────────────────────────
type SkillFolder = 'scripts' | 'references' | 'assets'
const FOLDER_ORDER: SkillFolder[] = ['scripts', 'references', 'assets']
const FOLDER_HINT: Record<SkillFolder, string> = {
  scripts: 'Code Claude can run (Python, bash, …)',
  references: 'Reference docs Claude reads on demand',
  assets: 'Templates, fonts, icons used in output',
}
const files = ref<SkillFileSummary[]>([])
// null selection inside the MORE tab = no file picked yet (empty-state).
const selectedPath = ref<string | null>(null)
const fileContent = ref('')
const fileIsBinary = ref(false)
const fileSize = ref(0)
const fileLoading = ref(false)
const fileDirty = ref(false)
const fileError = ref('')

const filesByFolder = computed(() => {
  const out: Record<SkillFolder, SkillFileSummary[]> = {
    scripts: [], references: [], assets: [],
  }
  for (const f of files.value) {
    const root = f.path.split('/', 1)[0] as SkillFolder
    if (out[root]) out[root].push(f)
  }
  return out
})

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
  }
}
const audit = ref<{
  createdByName?: string
  createdAt?: string
  updatedByName?: string
  updatedAt?: string
}>({})

function defaultBody() {
  return `## Instructions

Describe what the skill does, step by step.
`
}

function fmt(d?: string | null) {
  if (!d) return ''
  return new Date(d).toLocaleString()
}

function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(2)} MB`
}

async function load() {
  if (!isEdit.value) return
  try {
    const p = await api.getPlugin(props.pluginName)
    const s = p.skills?.find(s => s.name === props.skillName)
    if (!s) {
      error.value = 'skill not found'
      return
    }
    name.value = s.name
    description.value = s.description
    body.value = s.body
    audit.value = {
      createdByName: s.createdByName,
      createdAt: s.createdAt,
      updatedByName: s.updatedByName,
      updatedAt: s.updatedAt,
    }
    await Promise.all([loadVersions(), loadFiles()])
  } catch (e: any) {
    error.value = e.message
  }
}

async function loadVersions() {
  if (!props.skillName) return
  versionsError.value = ''
  try {
    versions.value = await api.skillVersions(props.pluginName, props.skillName)
  } catch (e: any) {
    versionsError.value = e.message
  }
}

async function loadFiles() {
  if (!props.skillName) return
  try {
    files.value = await api.listSkillFiles(props.pluginName, props.skillName)
  } catch (e: any) {
    error.value = e.message
  }
}

function clearFileSelection() {
  selectedPath.value = null
  fileError.value = ''
  fileDirty.value = false
}

async function selectFile(path: string) {
  if (selectedPath.value === path) return
  selectedPath.value = path
  fileError.value = ''
  fileDirty.value = false
  fileLoading.value = true
  try {
    const f = await api.getSkillFile(props.pluginName, props.skillName!, path)
    fileContent.value = f.content
    fileIsBinary.value = f.isBinary
    fileSize.value = f.sizeBytes
  } catch (e: any) {
    fileError.value = e.message
  } finally {
    fileLoading.value = false
  }
}

async function saveCurrentFile() {
  if (!selectedPath.value || !props.skillName) return
  fileError.value = ''
  try {
    const saved = await api.putSkillFile(
      props.pluginName,
      props.skillName,
      selectedPath.value,
      { content: fileContent.value, isBinary: fileIsBinary.value },
    )
    fileSize.value = saved.sizeBytes
    fileDirty.value = false
    await Promise.all([loadFiles(), loadVersions()])
  } catch (e: any) {
    fileError.value = e.message
  }
}

async function deleteCurrentFile() {
  if (!selectedPath.value || !props.skillName) return
  const ok = await confirm({
    title: 'Delete file',
    message: `Delete ${selectedPath.value}? This creates a new version, which you can revert if needed.`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteSkillFile(props.pluginName, props.skillName, selectedPath.value)
    clearFileSelection()
    await Promise.all([loadFiles(), loadVersions()])
  } catch (e: any) {
    fileError.value = e.message
  }
}

function downloadCurrentFile() {
  if (!selectedPath.value) return
  let blob: Blob
  if (fileIsBinary.value) {
    const bin = atob(fileContent.value)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    blob = new Blob([bytes], { type: 'application/octet-stream' })
  } else {
    blob = new Blob([fileContent.value], { type: 'text/plain;charset=utf-8' })
  }
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = selectedPath.value.split('/').pop() || 'file'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

const FILENAME_RE = /^[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)*$/

async function promptNewFile(folder: SkillFolder) {
  const raw = window.prompt(
    `New file under ${folder}/\nEnter relative path (e.g. build.py or sub/util.sh):`,
  )
  if (!raw) return
  const trimmed = raw.trim().replace(/^\/+/, '')
  if (!FILENAME_RE.test(trimmed)) {
    fileError.value = `invalid filename: ${trimmed}`
    return
  }
  const path = `${folder}/${trimmed}`
  if (files.value.some(f => f.path === path)) {
    await selectFile(path)
    return
  }
  try {
    await api.putSkillFile(props.pluginName, props.skillName!, path, {
      content: '',
      isBinary: false,
    })
    await Promise.all([loadFiles(), loadVersions()])
    await selectFile(path)
  } catch (e: any) {
    fileError.value = e.message
  }
}

function fileInputRefs(): Record<SkillFolder, HTMLInputElement | null> {
  return {
    scripts: scriptsInput.value,
    references: referencesInput.value,
    assets: assetsInput.value,
  }
}
const scriptsInput = ref<HTMLInputElement | null>(null)
const referencesInput = ref<HTMLInputElement | null>(null)
const assetsInput = ref<HTMLInputElement | null>(null)

function triggerUpload(folder: SkillFolder) {
  fileInputRefs()[folder]?.click()
}

async function onUploadChange(folder: SkillFolder, ev: Event) {
  const input = ev.target as HTMLInputElement
  if (!input.files || !props.skillName) return
  await uploadList(folder, input.files)
  input.value = ''
}

function isProbablyUtf8(bytes: Uint8Array): boolean {
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    return true
  } catch {
    return false
  }
}

function base64FromBytes(bytes: Uint8Array): string {
  // Chunk to avoid call stack limits on large files.
  let binary = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

async function uploadList(folder: SkillFolder, list: FileList) {
  fileError.value = ''
  let lastPath: string | null = null
  for (const file of Array.from(list)) {
    const safe = file.name.replace(/^.*[\\/]/, '')
    if (!FILENAME_RE.test(safe)) {
      fileError.value = `skipped invalid filename: ${file.name}`
      continue
    }
    const path = `${folder}/${safe}`
    try {
      const buf = await file.arrayBuffer()
      const bytes = new Uint8Array(buf)
      const binary = !isProbablyUtf8(bytes)
      const content = binary
        ? base64FromBytes(bytes)
        : new TextDecoder().decode(bytes)
      await api.putSkillFile(props.pluginName, props.skillName!, path, {
        content,
        isBinary: binary,
      })
      lastPath = path
    } catch (e: any) {
      fileError.value = `${file.name}: ${e.message}`
    }
  }
  await Promise.all([loadFiles(), loadVersions()])
  if (lastPath) await selectFile(lastPath)
}

async function onDrop(folder: SkillFolder, ev: DragEvent) {
  ev.preventDefault()
  if (!props.skillName || !ev.dataTransfer?.files) return
  await uploadList(folder, ev.dataTransfer.files)
}

async function submit() {
  error.value = ''
  loading.value = true
  try {
    if (isEdit.value) {
      await api.updateSkill(props.pluginName, props.skillName!, {
        description: description.value,
        body: body.value,
      })
      await Promise.all([loadVersions(), loadFiles()])
    } else {
      await api.createSkill(props.pluginName, {
        name: name.value,
        description: description.value,
        body: body.value,
      })
      router.push(`/plugins/${props.pluginName}`)
    }
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
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
  } catch (e: any) {
    validationError.value = e.message
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
    audit.value = {
      createdByName: s.createdByName,
      createdAt: s.createdAt,
      updatedByName: s.updatedByName,
      updatedAt: s.updatedAt,
    }
    await Promise.all([loadVersions(), loadFiles()])
    // Reload the currently-open file if it still exists, otherwise drop the
    // selection so we never display stale or missing-file content.
    if (selectedPath.value) {
      if (files.value.some(f => f.path === selectedPath.value)) {
        await selectFile(selectedPath.value)
      } else {
        clearFileSelection()
      }
    }
  } catch (e: any) {
    error.value = e.message
  }
}

onMounted(load)
</script>

<template>
  <h1>{{ isEdit ? `Edit skill: ${skillName}` : 'New skill' }}</h1>

  <!-- Create mode: simple single-column form (no file tree until skill exists) -->
  <div v-if="!isEdit" class="card">
    <form @submit.prevent="submit">
      <label>Skill name (slug, lowercase, [a-z0-9-])</label>
      <input
        v-model="name"
        required
        pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]"
      />

      <label>Description (used by Claude to decide when to use this skill)</label>
      <input v-model="description" required />

      <label>Body (Markdown — becomes the contents of SKILL.md after the frontmatter)</label>
      <textarea v-model="body" />

      <div v-if="error" class="error">{{ error }}</div>
      <div class="row" style="margin-top: 16px; gap: 8px; flex-wrap: wrap">
        <button type="submit" :disabled="loading">
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
        <RouterLink :to="`/plugins/${pluginName}`" class="btn secondary">Cancel</RouterLink>
      </div>
      <p class="muted" style="margin-top: 12px">
        Supporting files (scripts/, references/, assets/) can be added after the skill is created.
      </p>
    </form>
  </div>

  <!-- Edit mode: tabs -->
  <div v-else>
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
        <input v-model="description" required />

        <label>Body (Markdown — becomes the contents of SKILL.md after the frontmatter)</label>
        <textarea v-model="body" />

        <div v-if="error" class="error">{{ error }}</div>
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
          <RouterLink :to="`/plugins/${pluginName}`" class="btn secondary">Cancel</RouterLink>
        </div>
      </form>
    </div>

    <!-- MORE tab: file tree + per-file editor -->
    <div v-else class="skill-editor">
      <aside class="file-tree card">
        <p class="muted" style="margin-top: 0">
          Optional supporting files Claude can use alongside SKILL.md. Most skills don't need any.
        </p>
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
            <input
              v-if="folder === 'scripts'"
              ref="scriptsInput"
              type="file"
              multiple
              style="display: none"
              @change="onUploadChange(folder, $event)"
            />
            <input
              v-else-if="folder === 'references'"
              ref="referencesInput"
              type="file"
              multiple
              style="display: none"
              @change="onUploadChange(folder, $event)"
            />
            <input
              v-else
              ref="assetsInput"
              type="file"
              multiple
              style="display: none"
              @change="onUploadChange(folder, $event)"
            />
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
          <p v-else-if="fileError" class="error">{{ fileError }}</p>

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
    <p v-if="validationError" class="error">{{ validationError }}</p>

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
          :key="i"
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

  <div v-if="isEdit" class="card">
    <h2 style="margin-top: 0">Audit</h2>
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
  </div>

  <div v-if="isEdit" class="card">
    <h2 style="margin-top: 0">Edit history</h2>
    <p v-if="versionsError" class="error">{{ versionsError }}</p>
    <p v-else-if="versions.length === 0" class="muted">No history yet.</p>
    <table v-else>
      <thead>
        <tr>
          <th>Version</th>
          <th>Action</th>
          <th>By</th>
          <th>When</th>
          <th>Description</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="v in versions" :key="v.id">
          <td>{{ v.version }}</td>
          <td><span class="badge">{{ v.action }}</span></td>
          <td>{{ v.editedByName || '—' }}</td>
          <td class="muted" style="white-space: nowrap">{{ fmt(v.editedAt) }}</td>
          <td>{{ v.description }}</td>
          <td style="text-align: right">
            <button
              v-if="v.action !== 'delete'"
              class="secondary"
              type="button"
              @click="revert(v.version)"
            >Revert</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
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
</style>
