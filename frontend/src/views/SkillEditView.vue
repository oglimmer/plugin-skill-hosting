<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'

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

function defaultBody() {
  return `## Instructions

Describe what the skill does, step by step.
`
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
  } catch (e: any) {
    error.value = e.message
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
      })
    } else {
      await api.createSkill(props.pluginName, {
        name: name.value,
        description: description.value,
        body: body.value,
      })
    }
    router.push(`/plugins/${props.pluginName}`)
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <h1>{{ isEdit ? `Edit skill: ${skillName}` : 'New skill' }}</h1>
  <div class="card">
    <form @submit.prevent="submit">
      <label>Skill name (slug, lowercase, [a-z0-9-])</label>
      <input
        v-model="name"
        :disabled="isEdit"
        required
        pattern="[a-z0-9][a-z0-9-]{1,62}[a-z0-9]"
      />

      <label>Description (used by Claude to decide when to use this skill)</label>
      <input v-model="description" required />

      <label>Body (Markdown — becomes the contents of SKILL.md after the frontmatter)</label>
      <textarea v-model="body" />

      <div v-if="error" class="error">{{ error }}</div>
      <div class="row" style="margin-top: 16px">
        <button type="submit" :disabled="loading">
          {{ loading ? 'Saving…' : isEdit ? 'Save' : 'Create skill' }}
        </button>
        <RouterLink :to="`/plugins/${pluginName}`" class="btn secondary">Cancel</RouterLink>
      </div>
    </form>
  </div>
</template>
