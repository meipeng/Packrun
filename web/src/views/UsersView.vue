<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { projectApi, serverApi, userApi, type Project, type Server, type User } from '@/api'

const list = ref<User[]>([])
const servers = ref<Server[]>([])
const projects = ref<Project[]>([])
const projectsReady = ref(false)
const loading = ref(false)

const dialog = reactive({
  visible: false,
  saving: false,
  form: {
    id: 0,
    username: '',
    displayName: '',
    password: '',
    role: 'deployer',
    serverScope: '*',
    parentId: undefined as number | undefined,
    status: 'active',
    projectIds: [] as number[],
  },
})

const roleLabels: Record<string, string> = { admin: '管理员', deployer: '发布者', auditor: '审计员' }

// 子用户可见范围：从项目成员关系反推
const userProjects = computed<Record<number, string[]>>(() => {
  const map: Record<number, string[]> = {}
  projects.value.forEach((p) => {
    p.members.forEach((m) => {
      ;(map[m.id] = map[m.id] || []).push(p.name)
    })
  })
  return map
})

const adminUsers = computed(() => list.value.filter((u) => u.role === 'admin'))

function parentName(row: User) {
  if (!row.parentId) return '—（主账号）'
  const p = list.value.find((u) => u.id === row.parentId)
  return p ? p.username : `#${row.parentId}`
}

function projectChips(row: User) {
  return userProjects.value[row.id] || []
}

async function load() {
  loading.value = true
  try {
    const [u, s] = await Promise.all([userApi.list(), serverApi.list()])
    list.value = u
    servers.value = s
    try {
      projects.value = await projectApi.list()
      projectsReady.value = true
    } catch {
      projects.value = []
      projectsReady.value = false
    }
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  dialog.form = {
    id: 0,
    username: '',
    displayName: '',
    password: '',
    role: 'deployer',
    serverScope: '*',
    parentId: undefined,
    status: 'active',
    projectIds: [],
  }
  dialog.visible = true
}

function openEdit(row: User) {
  dialog.form = {
    id: row.id,
    username: row.username,
    displayName: row.displayName,
    password: '',
    role: row.role,
    serverScope: row.serverScope || '*',
    parentId: row.parentId || undefined,
    status: row.status,
    projectIds: projects.value.filter((p) => p.members.some((m) => m.id === row.id)).map((p) => p.id),
  }
  dialog.visible = true
}

async function save() {
  if (!dialog.form.username) {
    ElMessage.warning('请填写账号')
    return
  }
  if (!dialog.form.id && dialog.form.password.length < 8) {
    ElMessage.warning('初始口令至少 8 位')
    return
  }
  dialog.saving = true
  try {
    // 项目列表加载失败时不要提交授权，避免把既有授权清空。
    const payload: Record<string, unknown> = { ...dialog.form }
    if (!projectsReady.value) delete payload.projectIds
    if (dialog.form.id) {
      await userApi.update(dialog.form.id, payload)
    } else {
      await userApi.create(payload)
    }
    ElMessage.success('已保存')
    dialog.visible = false
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    dialog.saving = false
  }
}

function scopeText(scope: string) {
  if (!scope || scope === '*') return '全部服务器'
  const ids = scope.split(',').map((s) => Number(s.trim()))
  const names = ids.map((id) => servers.value.find((s) => s.id === id)?.name || `#${id}`)
  return names.join('、')
}

onMounted(load)
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">用户与权限</h2>
        <p class="pr-subtitle">角色决定动作范围，数据范围决定可见服务器；动态口令在个人设置中绑定。</p>
      </div>
      <el-button type="primary" :icon="Plus" @click="openCreate">新增用户</el-button>
    </div>

    <div class="pr-card">
      <el-table v-loading="loading" :data="list" style="width: 100%">
        <el-table-column label="账号" min-width="170">
          <template #default="{ row }">
            <div class="who">{{ row.displayName || row.username }}</div>
            <div class="account pr-mono">{{ row.username }}</div>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.role === 'admin' ? 'danger' : row.role === 'auditor' ? 'info' : 'primary'" effect="light">
              {{ roleLabels[row.role] || row.role }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="归属主账号" width="130">
          <template #default="{ row }">
            <span :class="row.parentId ? 'parent' : 'time'">{{ parentName(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="授权项目" min-width="150">
          <template #default="{ row }">
            <template v-if="projectChips(row).length">
              <el-tag v-for="name in projectChips(row)" :key="name" size="small" effect="plain" class="proj-chip">{{ name }}</el-tag>
            </template>
            <span v-else-if="row.role === 'admin'" class="time">全部项目</span>
            <span v-else class="time">—</span>
          </template>
        </el-table-column>
        <el-table-column label="数据范围" min-width="150">
          <template #default="{ row }">{{ scopeText(row.serverScope) }}</template>
        </el-table-column>
        <el-table-column label="动态口令" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.otpEnabled ? 'success' : 'warning'" effect="plain">
              {{ row.otpEnabled ? '已绑定' : '未绑定' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <span :class="row.status === 'active' ? 'on' : 'off'">{{ row.status === 'active' ? '启用' : '禁用' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最近登录" width="170">
          <template #default="{ row }">
            <span class="time">{{ row.lastLoginAt ? new Date(row.lastLoginAt).toLocaleString('zh-CN', { hour12: false }) : '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="dialog.visible" :title="dialog.form.id ? '编辑用户' : '新增子用户'" width="540px">
      <el-form label-width="92px">
        <el-form-item label="账号" required>
          <el-input v-model="dialog.form.username" :disabled="!!dialog.form.id" placeholder="zhangsan" />
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="dialog.form.displayName" placeholder="张三" />
        </el-form-item>
        <el-form-item :label="dialog.form.id ? '重置口令' : '初始口令'">
          <el-input v-model="dialog.form.password" type="password" show-password :placeholder="dialog.form.id ? '留空表示不修改' : '至少 8 位'" />
        </el-form-item>
        <el-form-item label="角色">
          <el-radio-group v-model="dialog.form.role">
            <el-radio-button value="deployer">发布者</el-radio-button>
            <el-radio-button value="auditor">审计员</el-radio-button>
            <el-radio-button value="admin">管理员</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="归属主账号">
          <el-select v-model="dialog.form.parentId" style="width: 100%" clearable placeholder="不选择表示自身为主账号">
            <el-option v-for="a in adminUsers" :key="a.id" :label="`${a.displayName || a.username}（${a.username}）`" :value="a.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="授权项目">
          <el-select v-model="dialog.form.projectIds" multiple filterable style="width: 100%" placeholder="选择该账号可见的项目">
            <el-option v-for="p in projects" :key="p.id" :label="p.name" :value="p.id" />
          </el-select>
          <div class="form-tip">仅被授权的子账户可在项目工作台看到对应项目；管理员默认可见全部项目。</div>
        </el-form-item>
        <el-form-item label="数据范围">
          <el-select v-model="dialog.form.serverScope" style="width: 100%">
            <el-option label="全部服务器" value="*" />
            <el-option
              v-for="s in servers"
              :key="s.id"
              :label="`${s.name}（${s.env}）`"
              :value="String(s.id)"
            />
          </el-select>
          <div class="form-tip">当前仅支持单一范围；多服务器可保留 * 或用逗号分隔 ID。</div>
        </el-form-item>
        <el-form-item label="状态" v-if="dialog.form.id">
          <el-switch v-model="dialog.form.status" active-value="active" inactive-value="disabled" active-text="启用" inactive-text="禁用" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.who {
  font-weight: 600;
  font-size: 13px;
}

.account {
  color: var(--pr-sub);
  margin-top: 2px;
}

.on {
  color: #1f7a45;
  font-size: 13px;
}

.off {
  color: #9aa1ae;
  font-size: 13px;
}

.parent {
  color: #4b5563;
  font-size: 13px;
}

.parent::before {
  content: '↳ ';
  color: #9aa1ae;
}

.proj-chip {
  margin: 1px 4px 1px 0;
}

.time {
  font-size: 12px;
  color: var(--pr-sub);
}

.form-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 4px;
}
</style>
