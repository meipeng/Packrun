<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Document,
  Folder,
  List,
  Monitor,
  Odometer,
  Setting,
  Tickets,
  UploadFilled,
  User as UserIcon,
} from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { overviewApi } from '@/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const deployMode = ref('')

const menu = computed(() => {
  const items = [
    { path: '/', label: '概览', icon: Odometer },
    { path: '/servers', label: '服务器', icon: Monitor },
    { path: '/projects', label: '项目工作台', icon: Folder, roles: ['admin', 'deployer'] },
    { path: '/deploy', label: '部署向导', icon: UploadFilled, roles: ['admin', 'deployer'] },
    { path: '/tasks', label: '任务', icon: List },
    { path: '/scripts', label: '脚本', icon: Document },
    { path: '/audit', label: '审计日志', icon: Tickets, roles: ['admin', 'auditor'] },
    { path: '/users', label: '用户与权限', icon: UserIcon, roles: ['admin'] },
  ]
  return items.filter((i) => !i.roles || (auth.user && i.roles.includes(auth.user.role)))
})

const activeMenu = computed(() => {
  if (route.path.startsWith('/tasks/')) return '/tasks'
  return route.path
})

const pageTitle = computed(() => (route.meta.title as string) || 'Packrun')

onMounted(async () => {
  try {
    const data = await overviewApi.get()
    deployMode.value = data.deployMode
  } catch {
    deployMode.value = ''
  }
})

function onCommand(cmd: string) {
  if (cmd === 'logout') {
    auth.logout()
    router.push({ name: 'login' })
    return
  }
  router.push('/profile')
}
</script>

<template>
  <el-container class="layout">
    <el-aside width="230px" class="aside">
      <div class="brand">
        <div class="brand-mark">P</div>
        <div>
          <div class="brand-name">Packrun</div>
          <div class="brand-sub">上传部署控制台</div>
        </div>
      </div>

      <el-menu :default-active="activeMenu" router class="menu">
        <el-menu-item v-for="item in menu" :key="item.path" :index="item.path">
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </el-menu-item>
      </el-menu>

      <div class="aside-foot">
        <div class="mode-chip" :class="deployMode">
          {{ deployMode === 'ssh' ? 'SSH 远程执行' : deployMode === 'local' ? '本地沙箱模式' : '未连接' }}
        </div>
        <div class="tip">中控机 · v0.1</div>
      </div>
    </el-aside>

    <el-container>
      <el-header class="header">
        <div class="crumb">
          <span class="crumb-root">{{ pageTitle }}</span>
          <span v-if="route.path.startsWith('/tasks/')" class="crumb-sub">/ 执行详情</span>
        </div>
        <el-dropdown trigger="click" @command="onCommand">
          <div class="user-box">
            <el-avatar :size="28" class="avatar">{{ (auth.displayName || 'U').slice(0, 1) }}</el-avatar>
            <div class="user-meta">
              <span class="user-name">{{ auth.displayName }}</span>
              <span class="user-role">{{ auth.user?.role === 'admin' ? '管理员' : auth.user?.role === 'auditor' ? '审计员' : '发布者' }}</span>
            </div>
            <el-icon class="caret"><Setting /></el-icon>
          </div>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="profile">个人设置</el-dropdown-item>
              <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </el-header>

      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.layout {
  height: 100%;
}

.aside {
  background: #ffffff;
  border-right: 1px solid var(--pr-line);
  display: flex;
  flex-direction: column;
  padding: 18px 14px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 8px 18px;
}

.brand-mark {
  width: 34px;
  height: 34px;
  border-radius: 9px;
  background: linear-gradient(135deg, #2f6bff, #6a9bff);
  color: #fff;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
}

.brand-name {
  font-weight: 700;
  font-size: 15px;
  letter-spacing: 0.4px;
}

.brand-sub {
  font-size: 11px;
  color: var(--pr-sub);
  margin-top: 2px;
}

.menu {
  border-right: none;
  flex: 1;
}

.menu :deep(.el-menu-item) {
  height: 42px;
  border-radius: 9px;
  margin-bottom: 4px;
  font-size: 14px;
}

.menu :deep(.el-menu-item.is-active) {
  background: var(--pr-brand-soft);
  color: var(--pr-brand);
  font-weight: 600;
}

.aside-foot {
  padding: 12px 8px 0;
  border-top: 1px dashed var(--pr-line);
}

.mode-chip {
  display: inline-block;
  font-size: 11px;
  padding: 4px 9px;
  border-radius: 20px;
  background: #eef1f6;
  color: #5b6474;
}

.mode-chip.ssh {
  background: #eaf6ee;
  color: #1f7a45;
}

.mode-chip.local {
  background: #fff4e5;
  color: #9a5b00;
}

.tip {
  margin-top: 8px;
  font-size: 11px;
  color: #9aa1ae;
}

.header {
  height: 60px;
  background: #fff;
  border-bottom: 1px solid var(--pr-line);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
}

.crumb-root {
  font-size: 15px;
  font-weight: 600;
}

.crumb-sub {
  font-size: 13px;
  color: var(--pr-sub);
  margin-left: 6px;
}

.user-box {
  display: flex;
  align-items: center;
  gap: 9px;
  cursor: pointer;
  padding: 5px 8px;
  border-radius: 9px;
}

.user-box:hover {
  background: #f4f6fb;
}

.avatar {
  background: #2f6bff;
  font-size: 12px;
}

.user-meta {
  display: flex;
  flex-direction: column;
  line-height: 1.2;
}

.user-name {
  font-size: 13px;
  font-weight: 600;
}

.user-role {
  font-size: 11px;
  color: var(--pr-sub);
}

.caret {
  color: #9aa1ae;
  font-size: 13px;
}

.main {
  padding: 0;
  overflow-y: auto;
}
</style>
