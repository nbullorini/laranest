<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NInput } from 'naive-ui'
import { api, setToken } from './api'

// Shown when the panel has no valid token, e.g. opened from a bookmark in a
// new browser. `laranest open` signs it in; pasting the token works too.
const token = ref('')
const error = ref('')
const checking = ref(false)

async function signIn() {
  error.value = ''
  checking.value = true
  setToken(token.value.trim())
  try {
    await api.system()
  } catch {
    setToken('')
    error.value = 'That token is not this Laranest’s.'
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <main class="signin stack">
    <div class="brand"><img src="/logo.png" alt="Laranest" class="logo" /> <span class="brand-name">Laranest</span></div>
    <h2>Open the panel from a terminal</h2>
    <p class="muted">
      Only the user running this Laranest can use its panel. In a terminal of that user, run
      <code>laranest open</code>: it opens this page signed in.
    </p>
    <form class="stack" @submit.prevent="signIn">
      <span class="muted small">Or paste its token (the <code>token</code> file in Laranest’s config folder):</span>
      <div class="row">
        <NInput v-model:value="token" type="password" placeholder="Token" show-password-on="click" />
        <NButton type="primary" attr-type="submit" :loading="checking" :disabled="!token.trim()">Sign in</NButton>
      </div>
      <span v-if="error" class="error small">{{ error }}</span>
    </form>
  </main>
</template>

<style scoped>
.signin {
  max-width: 520px;
  margin: 12vh auto 0;
  padding: 0 16px;
  gap: 12px;
}
.brand { font-weight: 700; font-size: 1.1rem; color: var(--brand); display: flex; gap: 8px; align-items: center; }
.logo { width: 32px; height: 32px; }
h2 { margin: 0; }
.row {
  display: flex;
  gap: 8px;
}
.error {
  color: var(--n-error-color, #d03050);
}
</style>
