<script setup lang="ts">
import { RouterLink, RouterView, useRouter } from 'vue-router'
import { currentUser, isAuthenticated, isAdmin, clearSession } from './session'

const router = useRouter()

function logout() {
  clearSession()
  router.push('/login')
}
</script>

<template>
  <div class="app">
    <header class="topbar">
      <h1 class="brand">📈 StockBook</h1>
      <nav class="nav">
        <RouterLink v-if="isAuthenticated" to="/positions" class="nav-link">Holdings</RouterLink>
        <RouterLink v-if="isAuthenticated" to="/transactions" class="nav-link">Ledger</RouterLink>
        <RouterLink v-if="isAdmin" to="/users" class="nav-link">Users</RouterLink>
      </nav>
      <div class="account">
        <template v-if="isAuthenticated && currentUser">
          <RouterLink to="/account" class="who"
            >{{ currentUser.username }} <span class="role">{{ currentUser.role }}</span></RouterLink
          >
          <button class="logout" @click="logout">Logout</button>
        </template>
        <RouterLink v-else to="/login" class="nav-link">Login</RouterLink>
      </div>
    </header>

    <main class="content">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 24px;
  background: #0f172a;
  color: #fff;
}
.brand {
  margin: 0;
  font-size: 20px;
}
.nav {
  display: flex;
  gap: 8px;
  margin-right: auto;
  margin-left: 24px;
}
.account {
  display: flex;
  align-items: center;
  gap: 12px;
}
.who {
  color: #cbd5e1;
  font-size: 14px;
  text-decoration: none;
}
.who:hover {
  color: #fff;
}
.role {
  display: inline-block;
  margin-left: 4px;
  padding: 1px 8px;
  border-radius: 999px;
  background: #334155;
  color: #f8fafc;
  font-size: 11px;
  text-transform: capitalize;
}
.logout {
  background: transparent;
  border: 1px solid #475569;
  color: #cbd5e1;
  padding: 6px 12px;
  border-radius: 8px;
  cursor: pointer;
  font-weight: 500;
}
.logout:hover {
  background: #334155;
  color: #fff;
}
.nav-link {
  color: #cbd5e1;
  text-decoration: none;
  padding: 8px 14px;
  border-radius: 8px;
  font-weight: 500;
}
.nav-link:hover {
  background: #334155;
  color: #fff;
}
.nav-link.router-link-active {
  background: #0d9488;
  color: #fff;
}
.content {
  max-width: 1100px;
  margin: 0 auto;
  padding: 24px;
}
</style>
