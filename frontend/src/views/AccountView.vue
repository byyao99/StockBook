<script setup lang="ts">
import { ref } from 'vue'
import { authApi } from '../api/client'
import { currentUser, setSession } from '../session'

const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const error = ref('')
const success = ref('')
const loading = ref(false)

async function submit() {
  error.value = ''
  success.value = ''
  if (newPassword.value !== confirmPassword.value) {
    error.value = 'New passwords do not match.'
    return
  }
  loading.value = true
  try {
    // The change revokes the current token; adopt the fresh session from the
    // response so the user stays signed in.
    const session = await authApi.changePassword(oldPassword.value, newPassword.value)
    setSession(session.token, session.user)
    success.value = 'Password changed successfully.'
    oldPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
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
      <h2 class="section-title">Account</h2>
      <p v-if="currentUser" class="muted who">
        Signed in as <strong>{{ currentUser.username }}</strong> ({{ currentUser.role }})
      </p>

      <h3 class="sub">Change Password</h3>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="success" class="success">{{ success }}</p>

      <form @submit.prevent="submit">
        <div class="field">
          <label>Current password</label>
          <input v-model="oldPassword" type="password" required autocomplete="current-password" />
        </div>
        <div class="field">
          <label>New password</label>
          <input
            v-model="newPassword"
            type="password"
            required
            minlength="8"
            maxlength="72"
            autocomplete="new-password"
          />
        </div>
        <div class="field">
          <label>Confirm new password</label>
          <input
            v-model="confirmPassword"
            type="password"
            required
            minlength="8"
            maxlength="72"
            autocomplete="new-password"
          />
        </div>
        <button type="submit" class="btn-primary full" :disabled="loading">
          {{ loading ? 'Please wait…' : 'Change Password' }}
        </button>
      </form>
      <p class="hint muted">
        Use at least two of: lowercase letter, uppercase letter, digit.
      </p>
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
.who {
  margin-top: -8px;
}
.sub {
  margin: 16px 0 8px;
  font-size: 15px;
}
.success {
  color: #16a34a;
  font-weight: 600;
}
.full {
  width: 100%;
  margin-top: 4px;
}
.hint {
  text-align: center;
  font-size: 12px;
  margin-top: 8px;
}
</style>
