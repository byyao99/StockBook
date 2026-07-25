<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { authApi } from '../api/client'
import { setSession } from '../session'

const route = useRoute()
const router = useRouter()

const mode = ref<'login' | 'register'>('login')
const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)

function toggleMode() {
  mode.value = mode.value === 'login' ? 'register' : 'login'
  error.value = ''
}

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const res =
      mode.value === 'login'
        ? await authApi.login(username.value, password.value)
        : await authApi.register(username.value, password.value)
    setSession(res.token, res.user)
    const redirect = (route.query.redirect as string) || '/positions'
    router.push(redirect)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="auth-wrap">
    <section class="card auth-card">
      <h2 class="section-title">{{ mode === 'login' ? 'Sign In' : 'Create Account' }}</h2>
      <p v-if="error" class="error">{{ error }}</p>

      <form @submit.prevent="submit">
        <div class="field">
          <label>Username</label>
          <input v-model="username" required minlength="3" maxlength="60" autocomplete="username" />
        </div>
        <div class="field">
          <label>Password</label>
          <input
            v-model="password"
            type="password"
            required
            minlength="8"
            maxlength="72"
            :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
          />
        </div>
        <button type="submit" class="btn-primary full" :disabled="loading">
          {{ loading ? 'Please wait…' : mode === 'login' ? 'Sign In' : 'Register' }}
        </button>
      </form>

      <p class="switch muted">
        {{ mode === 'login' ? 'New here?' : 'Already have an account?' }}
        <a href="#" @click.prevent="toggleMode">
          {{ mode === 'login' ? 'Create an account' : 'Sign in' }}
        </a>
      </p>
      <p class="hint muted">New accounts are created as customers.</p>
    </section>
  </div>
</template>

<style scoped>
.auth-wrap {
  display: flex;
  justify-content: center;
}
.auth-card {
  width: 100%;
  max-width: 380px;
}
.full {
  width: 100%;
  margin-top: 4px;
}
.switch {
  text-align: center;
  margin-top: 16px;
}
.switch a {
  color: #f97316;
  font-weight: 600;
}
.hint {
  text-align: center;
  font-size: 12px;
  margin-top: 4px;
}
</style>
