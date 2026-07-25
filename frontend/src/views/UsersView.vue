<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { userApi } from '../api/client'
import { currentUser } from '../session'
import PaginationBar from '../components/PaginationBar.vue'
import { ROLES } from '../types'
import type { AuthUser, Role } from '../types'

const PAGE_SIZE = 20

const users = ref<AuthUser[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')
const success = ref('')

const form = reactive<{ username: string; password: string; role: Role }>({
  username: '',
  password: '',
  role: 'user',
})

function resetForm() {
  Object.assign(form, { username: '', password: '', role: 'user' })
}

// Inline password-reset state: the row currently being reset and its input.
const resettingId = ref<string | null>(null)
const resetValue = ref('')

function startReset(user: AuthUser) {
  resettingId.value = user.id
  resetValue.value = ''
  error.value = ''
  success.value = ''
}

function cancelReset() {
  resettingId.value = null
  resetValue.value = ''
}

async function submitReset(user: AuthUser) {
  error.value = ''
  success.value = ''
  try {
    await userApi.resetPassword(user.id, resetValue.value)
    success.value = `Password reset for ${user.username}.`
    cancelReset()
  } catch (e) {
    error.value = (e as Error).message
  }
}

// isSelf marks the signed-in admin, whose own account cannot be edited/deleted.
function isSelf(user: AuthUser): boolean {
  return user.id === currentUser.value?.id
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await userApi.list(PAGE_SIZE, offset.value)
    users.value = page.items
    total.value = page.pagination.total
    if (users.value.length === 0 && offset.value > 0) {
      offset.value = Math.max(0, offset.value - PAGE_SIZE)
      await load()
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function changePage(newOffset: number) {
  offset.value = newOffset
  load()
}

async function createUser() {
  error.value = ''
  success.value = ''
  try {
    await userApi.create({ username: form.username, password: form.password, role: form.role })
    success.value = `Created ${form.username} (${form.role}).`
    resetForm()
    offset.value = 0
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function changeRole(user: AuthUser, role: Role) {
  if (role === user.role) return
  error.value = ''
  success.value = ''
  try {
    await userApi.updateRole(user.id, role)
    await load()
  } catch (e) {
    error.value = (e as Error).message
    await load() // revert the dropdown to the persisted value
  }
}

async function remove(user: AuthUser) {
  if (!confirm(`Delete user "${user.username}"?`)) return
  error.value = ''
  success.value = ''
  try {
    await userApi.remove(user.id)
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function formatTime(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : '—'
}

onMounted(load)
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <section class="card">
      <h2 class="section-title">New User</h2>
      <form @submit.prevent="createUser">
        <div class="grid">
          <div class="field">
            <label>Username</label>
            <input v-model="form.username" required minlength="3" maxlength="60" />
          </div>
          <div class="field">
            <label>Password</label>
            <input v-model="form.password" type="password" required minlength="8" maxlength="72" />
          </div>
          <div class="field">
            <label>Role</label>
            <select v-model="form.role">
              <option v-for="r in ROLES" :key="r" :value="r">{{ r }}</option>
            </select>
          </div>
        </div>
        <div class="actions">
          <button type="submit" class="btn-primary">Create User</button>
        </div>
      </form>
    </section>

    <section class="card">
      <h2 class="section-title">Users ({{ total }})</h2>
      <p v-if="loading" class="muted">Loading…</p>
      <table v-else>
        <thead>
          <tr>
            <th>Username</th>
            <th>Role</th>
            <th>Created</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="user in users" :key="user.id">
            <td>
              <strong>{{ user.username }}</strong>
              <span v-if="isSelf(user)" class="muted"> (you)</span>
            </td>
            <td>
              <select
                :value="user.role"
                :disabled="isSelf(user)"
                @change="changeRole(user, ($event.target as HTMLSelectElement).value as Role)"
              >
                <option v-for="r in ROLES" :key="r" :value="r">{{ r }}</option>
              </select>
            </td>
            <td>{{ formatTime(user.created_at) }}</td>
            <td class="row-actions">
              <template v-if="resettingId === user.id">
                <input
                  v-model="resetValue"
                  type="password"
                  placeholder="New password"
                  minlength="8"
                  maxlength="72"
                  class="reset-input"
                />
                <button class="btn-primary" @click="submitReset(user)">Save</button>
                <button @click="cancelReset">Cancel</button>
              </template>
              <template v-else>
                <button @click="startReset(user)">Reset password</button>
                <button class="btn-danger" :disabled="isSelf(user)" @click="remove(user)">Delete</button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
      <PaginationBar :limit="PAGE_SIZE" :offset="offset" :total="total" @change="changePage" />
    </section>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  gap: 12px;
}
.actions {
  margin-top: 8px;
}
.row-actions {
  display: flex;
  gap: 8px;
}
td select {
  width: auto;
}
.reset-input {
  width: 140px;
}
button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
