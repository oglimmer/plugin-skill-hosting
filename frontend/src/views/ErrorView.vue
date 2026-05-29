<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'

// Reusable, presentational error page. It works both as a route component
// (the catch-all 404 route, the /error route) and embedded inside a view that
// caught a failed load — pass the HTTP `code` (e.g. errStatus(e)) and an
// optional `details` string with the raw server message.
const props = withDefaults(
  defineProps<{
    code?: number
    title?: string
    message?: string
    details?: string
  }>(),
  { code: 404 },
)

const router = useRouter()

// Default copy per status family. `title`/`message` props override these so a
// caller can be more specific ("This plugin was deleted") when it knows more.
const PRESETS: Record<number, { title: string; message: string }> = {
  403: {
    title: 'Access denied',
    message: "You don't have permission to view this page.",
  },
  404: {
    title: 'Page not found',
    message:
      "We couldn't find the page you were looking for. It may have been moved, deleted, or never existed.",
  },
  500: {
    title: 'Something went wrong',
    message:
      'The server ran into an unexpected problem. Please try again in a moment.',
  },
}

const preset = computed(
  () =>
    PRESETS[props.code] ?? {
      title: 'Something went wrong',
      message: 'An unexpected error occurred while loading this page.',
    },
)

const title = computed(() => props.title ?? preset.value.title)
const message = computed(() => props.message ?? preset.value.message)

function goBack() {
  // history.state.back is null on a fresh tab / direct link — fall back home.
  if (window.history.state?.back) router.back()
  else router.push('/')
}
</script>

<template>
  <div class="error-page">
    <div class="card error-card">
      <p class="kicker">Error {{ code }}</p>
      <h1>{{ title }}</h1>
      <p class="lede">{{ message }}</p>

      <p v-if="details && details !== message" class="error-details">
        <code>{{ details }}</code>
      </p>

      <div class="row">
        <RouterLink class="btn" to="/">Back to home</RouterLink>
        <button type="button" class="secondary" @click="goBack">Go back</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.error-page {
  min-height: calc(100vh - 200px);
  display: grid;
  place-items: center;
  padding: 60px 24px;
}
.error-card {
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
.error-card h1 { margin: 0 0 18px; }
.lede {
  font-family: var(--serif);
  font-size: 17px;
  line-height: 1.55;
  color: var(--text-soft);
  margin: 0 0 24px;
}
.error-details {
  margin: 0 0 24px;
}
.error-details code {
  display: block;
  word-break: break-word;
}
.row {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
</style>
