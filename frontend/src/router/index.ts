import { createRouter, createWebHistory } from 'vue-router'
import PositionsView from '../views/PositionsView.vue'
import TransactionsView from '../views/TransactionsView.vue'
import InstrumentsView from '../views/InstrumentsView.vue'
import UsersView from '../views/UsersView.vue'
import AccountView from '../views/AccountView.vue'
import LoginView from '../views/LoginView.vue'
import { isAdmin, isAuthenticated } from '../session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/positions' },
    {
      path: '/positions',
      name: 'positions',
      component: PositionsView,
      meta: { requiresAuth: true },
    },
    {
      path: '/transactions',
      name: 'transactions',
      component: TransactionsView,
      meta: { requiresAuth: true },
    },
    {
      path: '/instruments',
      name: 'instruments',
      component: InstrumentsView,
      meta: { requiresAuth: true },
    },
    {
      path: '/users',
      name: 'users',
      component: UsersView,
      meta: { requiresAuth: true, requiresAdmin: true },
    },
    { path: '/account', name: 'account', component: AccountView, meta: { requiresAuth: true } },
    { path: '/login', name: 'login', component: LoginView },
  ],
})

// Gate routes that require a signed-in user (and, for some, an admin), and keep
// authenticated users away from the login page. This mirrors the backend's own
// rules; it is a convenience, not the enforcement — the API checks every request.
router.beforeEach((to) => {
  if (to.meta.requiresAuth && !isAuthenticated.value) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.meta.requiresAdmin && !isAdmin.value) return { name: 'positions' }
  if (to.name === 'login' && isAuthenticated.value) return { name: 'positions' }
})
