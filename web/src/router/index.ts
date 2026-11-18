import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { appBasePath } from '@/utils/base'

const router = createRouter({
  history: createWebHistory(appBasePath()),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { public: true },
    },
    {
      path: '/',
      component: () => import('@/layouts/AppLayout.vue'),
      children: [
        { path: '', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: '概览' } },
        {
          path: 'projects',
          name: 'projects',
          component: () => import('@/views/ProjectsView.vue'),
          meta: { title: '项目工作台', roles: ['admin', 'deployer'] },
        },
        { path: 'servers', name: 'servers', component: () => import('@/views/ServersView.vue'), meta: { title: '服务器' } },
        {
          path: 'deploy',
          name: 'deploy',
          component: () => import('@/views/DeployWizardView.vue'),
          meta: { title: '部署向导', roles: ['admin', 'deployer'] },
        },
        { path: 'tasks', name: 'tasks', component: () => import('@/views/TasksView.vue'), meta: { title: '任务' } },
        { path: 'tasks/:id', name: 'task-detail', component: () => import('@/views/TaskDetailView.vue'), meta: { title: '任务详情' } },
        { path: 'scripts', name: 'scripts', component: () => import('@/views/ScriptsView.vue'), meta: { title: '脚本' } },
        { path: 'audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { title: '审计日志', roles: ['admin', 'auditor'] } },
        { path: 'users', name: 'users', component: () => import('@/views/UsersView.vue'), meta: { title: '用户与权限', roles: ['admin'] } },
        { path: 'profile', name: 'profile', component: () => import('@/views/ProfileView.vue'), meta: { title: '个人设置' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()

  if (to.meta.public) {
    return true
  }
  if (!auth.token) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (!auth.user) {
    try {
      await auth.fetchMe()
    } catch {
      auth.logout()
      return { name: 'login' }
    }
  }
  const roles = to.meta.roles as string[] | undefined
  if (roles && auth.user && !roles.includes(auth.user.role)) {
    return { name: 'overview' }
  }
  return true
})

export default router
