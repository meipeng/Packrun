<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Folder, Plus, Setting, UploadFilled } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import {
  directoryApi,
  packageApi,
  projectApi,
  scriptApi,
  serverApi,
  userApi,
  type Directory,
  type Package,
  type Project,
  type ProjectHistoryItem,
  type Script,
  type Server,
  type User,
} from '@/api'
import { envMeta, formatDateTime, formatSize } from '@/utils/status'

const auth = useAuthStore()
const isAdmin = computed(() => auth.user?.role === 'admin')

// ---------- 数据 ----------
const projects = ref<Project[]>([])
const current = ref<Project | null>(null)
const history = ref<ProjectHistoryItem[]>([])
const packages = ref<Package[]>([])
const servers = ref<Server[]>([])
const allDirs = ref<Directory[]>([])
const users = ref<User[]>([])
const scripts = ref<Script[]>([])

const loading = ref(false)
const selectedBatch = ref('')
let watchedBatch = ''

const staged = ref<{ mode: 'file'; file: File } | { mode: 'pkg'; pkg: Package } | null>(null)
const uploadState = reactive({ active: false, phase: '' as '' | 'upload' | 'publish', pct: 0 })
const publishResult = ref<{ batchNo: string; version: string } | null>(null)
const publishError = ref('')
const uploadLabel = computed(() => {
  if (uploadState.phase === 'publish') return '校验通过，正在提交发布任务…'
  if (uploadState.pct >= 100) return '上传完成，正在校验文件格式并登记 SHA256…'
  return `上传中 ${uploadState.pct}%`
})

const dialog = reactive({
  createVisible: false,
  settingsVisible: false,
  rollbackVisible: false,
  publishVisible: false,
  saving: false,
  publishing: false,
  rollbacking: false,
  createForm: { name: '', code: '', description: '', scriptId: undefined as number | undefined },
  settingsForm: {
    directoryIds: [] as number[],
    memberIds: [] as number[],
    scriptId: undefined as number | undefined,
    policy: { backupBeforeDeploy: true, allowOverwrite: true, keepReleases: 5 },
  },
  otp: '',
})

let pollTimer: number | null = null

const hasProd = computed(() => !!current.value?.dirs.some((d) => d.serverEnv === 'prod'))
const preScripts = computed(() => scripts.value.filter((s) => s.phase === 'precheck'))
const postScripts = computed(() => scripts.value.filter((s) => s.phase === 'postdeploy'))

// 本次发布使用的脚本：默认取项目设置，可在发布前覆盖或清空。
const publishPreScriptId = ref<number | undefined>(undefined)
const publishScriptId = ref<number | undefined>(undefined)
const publishPreScriptName = computed(() => scripts.value.find((s) => s.id === publishPreScriptId.value)?.name || '')
const publishScriptName = computed(() => scripts.value.find((s) => s.id === publishScriptId.value)?.name || '')

watch(
  () => current.value?.id,
  () => {
    publishScriptId.value = current.value?.scriptId || undefined
    publishPreScriptId.value = undefined
  },
)
const canManage = computed(() => {
  const p = current.value
  if (!p || !auth.user) return false
  return isAdmin.value || p.ownerId === auth.user.id
})
const selectedHistory = computed(() => history.value.find((h) => h.batchNo === selectedBatch.value) || null)
const activeBatch = computed(() => history.value.find((h) => h.status === 'running') || history.value[0] || null)

// 选中批次即项目当前版本时（如仅有一条发布记录），回滚无意义，禁用按钮。
const selectedIsCurrent = computed(() =>
  !!selectedHistory.value && !!current.value && selectedHistory.value.projectVersion === current.value.version,
)
const rollbackDisabled = computed(() => {
  const h = selectedHistory.value
  if (!h) return true
  return h.status !== 'success' || selectedIsCurrent.value
})

const memberNames = computed(() => (current.value?.members || []).map((m) => m.displayName || m.username))

function fmtTime(iso: string) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('zh-CN', { hour12: false })
}

const statusMeta: Record<string, { label: string; type: 'success' | 'danger' | 'warning' | 'primary' | 'info' }> = {
  success: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'danger' },
  running: { label: '进行中', type: 'primary' },
  canceled: { label: '已取消', type: 'info' },
}

const envTag = (env: string) => envMeta(env) || { label: env, type: 'info' }

// ---------- 加载 ----------
async function loadProjects(keepSelection = true) {
  loading.value = true
  try {
    const list = await projectApi.list()
    projects.value = list
    if (!keepSelection || !list.find((p) => p.id === current.value?.id)) {
      current.value = list[0] || null
    } else {
      current.value = list.find((p) => p.id === current.value?.id) || null
    }
    if (current.value) {
      await refreshHistory()
      if (history.value.some((h) => h.status === 'running')) startPolling()
      else stopPolling()
    } else {
      history.value = []
    }
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function refreshHistory() {
  if (!current.value) return
  try {
    history.value = await projectApi.history(current.value.id)
  } catch {
    /* 轮询失败静默，下轮重试 */
  }
}

function startPolling() {
  if (pollTimer !== null) return
  const running = history.value.find((h) => h.status === 'running')
  if (running) watchedBatch = running.batchNo
  pollTimer = window.setInterval(async () => {
    await refreshHistory()
    if (!history.value.some((h) => h.status === 'running')) {
      stopPolling()
      const finished = history.value.find((h) => h.batchNo === watchedBatch)
      if (finished) {
        if (finished.status === 'success') {
          ElMessage.success(`发布完成：${finished.projectVersion}（批次 ${finished.batchNo}）`)
        } else if (finished.status === 'failed') {
          ElMessage.error(`发布失败：${finished.projectVersion}（批次 ${finished.batchNo}），请查看批次内任务日志`)
        } else {
          ElMessage.info(`发布已取消：${finished.projectVersion}（批次 ${finished.batchNo}）`)
        }
      }
      watchedBatch = ''
      // 批次结算后同步项目版本号
      const list = await projectApi.list()
      projects.value = list
      current.value = list.find((p) => p.id === current.value?.id) || list[0] || null
    }
  }, 1500)
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function selectProject(p: Project) {
  if (p.id === current.value?.id) return
  stopPolling()
  watchedBatch = ''
  publishResult.value = null
  publishError.value = ''
  current.value = p
  selectedBatch.value = ''
  staged.value = null
  refreshHistory().then(() => {
    if (history.value.some((h) => h.status === 'running')) startPolling()
  })
}

onMounted(async () => {
  await loadProjects(false)
  try {
    const [pk, sv, sc] = await Promise.all([packageApi.list(), serverApi.list(), scriptApi.list()])
    packages.value = pk
    servers.value = sv
    scripts.value = sc
    allDirs.value = await directoryApi.list()
    if (isAdmin.value) users.value = await userApi.list()
  } catch {
    /* 非管理员拉取用户列表会 403，忽略 */
  }
})

onBeforeUnmount(stopPolling)

// ---------- 新建项目 ----------
function openCreate() {
  dialog.createForm = { name: '', code: '', description: '', scriptId: undefined }
  dialog.createVisible = true
}

async function saveCreate() {
  if (!dialog.createForm.name.trim()) {
    ElMessage.warning('请填写项目名称')
    return
  }
  dialog.saving = true
  try {
    const p = await projectApi.create(dialog.createForm)
    ElMessage.success(`已创建项目「${p.name}」，请在项目设置中绑定更新目录与成员`)
    dialog.createVisible = false
    await loadProjects()
    selectProject(p)
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    dialog.saving = false
  }
}

// ---------- 项目设置 ----------
function openSettings() {
  const p = current.value
  if (!p) return
  dialog.settingsForm = {
    directoryIds: p.dirs.map((d) => d.id),
    memberIds: p.members.map((m) => m.id),
    scriptId: p.scriptId || undefined,
    policy: {
      backupBeforeDeploy: p.dirs[0]?.backupBeforeDeploy ?? true,
      allowOverwrite: p.dirs[0]?.allowOverwrite ?? true,
      keepReleases: p.dirs[0]?.keepReleases ?? 5,
    },
  }
  dialog.settingsVisible = true
}

async function saveSettings() {
  const p = current.value
  if (!p) return
  dialog.saving = true
  try {
    const payload: Record<string, unknown> = {
      directoryIds: dialog.settingsForm.directoryIds,
      scriptId: dialog.settingsForm.scriptId ?? 0,
    }
    if (isAdmin.value) {
      payload.memberIds = dialog.settingsForm.memberIds
      payload.policy = dialog.settingsForm.policy
    }
    const updated = await projectApi.update(p.id, payload)
    ElMessage.success('项目设置已保存')
    dialog.settingsVisible = false
    const idx = projects.value.findIndex((x) => x.id === p.id)
    if (idx >= 0) {
      projects.value[idx] = updated
      current.value = updated
      publishScriptId.value = updated.scriptId || undefined
    }
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    dialog.saving = false
  }
}

async function removeProject() {
  const p = current.value
  if (!p) return
  try {
    await ElMessageBox.confirm(`确定删除项目「${p.name}」？绑定关系与成员授权将移除，历史任务保留。`, '删除项目', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      confirmButtonClass: 'el-button--danger',
    })
  } catch {
    return
  }
  try {
    await projectApi.remove(p.id)
    ElMessage.success('项目已删除')
    dialog.settingsVisible = false
    current.value = null
    history.value = []
    await loadProjects(false)
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

// ---------- 部署空间 ----------
function onFileChange(file: any) {
  const raw: File | undefined = file?.raw
  if (!raw) return
  const name = raw.name.toLowerCase()
  if (!name.endsWith('.zip') && !name.endsWith('.jar')) {
    ElMessage.error('仅支持 .zip 与 .jar')
    return
  }
  if (raw.size > 200 * 1024 * 1024) {
    ElMessage.error('单包不得超过 200MB')
    return
  }
  publishError.value = ''
  staged.value = { mode: 'file', file: raw }
}

function pickRecent(pkg: Package) {
  publishError.value = ''
  staged.value = { mode: 'pkg', pkg }
}

async function clearStaged() {
  if (!staged.value) return
  // 本地上传前选中的文件仅需取消选择；已登记的历史包则连记录与仓库文件一起移除。
  if (staged.value.mode === 'pkg') {
    const pkg = staged.value.pkg
    try {
      await ElMessageBox.confirm(`将删除「${pkg.name}」及其仓库文件，且不可恢复。`, '移除安装包', {
        type: 'warning',
        confirmButtonText: '移除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger',
      })
    } catch {
      return
    }
    try {
      await packageApi.remove(pkg.id)
      packages.value = packages.value.filter((p) => p.id !== pkg.id)
      staged.value = null
      ElMessage.success('安装包已移除')
    } catch (e: any) {
      ElMessage.error(e.message)
    }
    return
  }
  staged.value = null
}

function dayKey(d: Date) {
  return `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, '0')}${String(d.getDate()).padStart(2, '0')}`
}

// 最近上传按 yyyyMMdd 分组：当天直接平铺，其余日期带分组标题。
const recentGroups = computed(() => {
  const groups = new Map<string, Package[]>()
  for (const p of packages.value) {
    const key = dayKey(new Date(p.createdAt))
    const items = groups.get(key)
    if (items) items.push(p)
    else groups.set(key, [p])
  }
  const today = dayKey(new Date())
  return [
    { key: today, isToday: true, items: groups.get(today) || [] },
    ...[...groups.entries()]
      .filter(([key]) => key !== today)
      .sort((a, b) => (a[0] < b[0] ? 1 : -1))
      .map(([key, items]) => ({ key, isToday: false, items })),
  ].filter((g) => g.items.length > 0)
})

const stagedText = computed(() => {
  if (!staged.value) return ''
  return staged.value.mode === 'file'
    ? `${staged.value.file.name}（${formatSize(staged.value.file.size)}）`
    : `${staged.value.pkg.name}（${formatSize(staged.value.pkg.size)}）`
})

const dirSummary = computed(() => {
  const dirs = current.value?.dirs || []
  if (!dirs.length) return '请先绑定更新目录'
  const envCount: Record<string, number> = {}
  dirs.forEach((d) => (envCount[d.serverEnv] = (envCount[d.serverEnv] || 0) + 1))
  const text = Object.entries(envCount)
    .map(([env, n]) => `${envMeta(env)?.label || env} ${n}`)
    .join(' / ')
  return `将发布至 ${dirs.length} 个目录（${text}）`
})

// ---------- 发布 ----------
function clickPublish() {
  const p = current.value
  if (!p) return
  publishError.value = ''
  publishResult.value = null
  if (!staged.value) {
    publishError.value = '请先拖入或点选发布包'
    ElMessage.warning(publishError.value)
    return
  }
  if (!p.dirs.length) {
    publishError.value = '该项目尚未绑定更新目录'
    ElMessage.warning(publishError.value)
    return
  }
  if (history.value.some((h) => h.status === 'running')) {
    publishError.value = '该项目仍有发布任务进行中，请等待完成或取消后再试'
    ElMessage.warning(publishError.value)
    return
  }
  dialog.otp = ''
  if (hasProd.value) dialog.publishVisible = true
  else doPublish()
}

async function doPublish() {
  const p = current.value
  if (!p || !staged.value) return
  if (hasProd.value && dialog.otp.length !== 6) {
    ElMessage.warning('生产环境需要输入 6 位动态口令')
    return
  }
  dialog.publishing = true
  publishError.value = ''
  publishResult.value = null
  uploadState.active = false
  uploadState.phase = ''
  uploadState.pct = 0
  try {
    let packageId: number
    if (staged.value.mode === 'file') {
      uploadState.active = true
      uploadState.phase = 'upload'
      const pkg = await packageApi.upload(staged.value.file, (pct) => (uploadState.pct = pct))
      packages.value.unshift(pkg)
      packageId = pkg.id
    } else {
      packageId = staged.value.pkg.id
    }
    uploadState.active = true
    uploadState.phase = 'publish'
    uploadState.pct = 100
    const res = await projectApi.publish(p.id, {
      packageId,
      preScriptId: publishPreScriptId.value ?? 0,
      scriptId: publishScriptId.value ?? 0,
      otp: dialog.otp || undefined,
    })
    publishResult.value = { batchNo: res.batchNo, version: res.version }
    ElMessage.success(`发布任务已受理：批次 ${res.batchNo}`)
    dialog.publishVisible = false
    staged.value = null
    selectedBatch.value = ''
    await refreshHistory()
    startPolling()
  } catch (e: any) {
    publishError.value = e.message
    ElMessage.error(e.message)
  } finally {
    dialog.publishing = false
    uploadState.active = false
    uploadState.phase = ''
    uploadState.pct = 0
  }
}

// ---------- 历史与回滚 ----------
function selectBatch(h: ProjectHistoryItem) {
  selectedBatch.value = selectedBatch.value === h.batchNo ? '' : h.batchNo
}

function clickRollback() {
  const h = selectedHistory.value
  if (!h) return
  if (h.status !== 'success') {
    ElMessage.warning('仅发布成功的记录可以回滚')
    return
  }
  if (current.value && h.projectVersion === current.value.version) {
    ElMessage.warning('项目当前已处于该版本')
    return
  }
  dialog.otp = ''
  dialog.rollbackVisible = true
}

async function doRollback() {
  const p = current.value
  const h = selectedHistory.value
  if (!p || !h) return
  if (hasProd.value && dialog.otp.length !== 6) {
    ElMessage.warning('生产环境需要输入 6 位动态口令')
    return
  }
  dialog.rollbacking = true
  try {
    await projectApi.rollback(p.id, { batchNo: h.batchNo, otp: dialog.otp || undefined })
    ElMessage.success(`回滚任务已受理，正在回退至 ${h.projectVersion}`)
    dialog.rollbackVisible = false
    selectedBatch.value = ''
    await refreshHistory()
    startPolling()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    dialog.rollbacking = false
  }
}

const dirLabel = (d: Directory) => {
  const s = servers.value.find((x) => x.id === d.serverId)
  return s ? `${s.name} · ${s.env} · ${d.path}` : d.path
}
</script>

<template>
  <div class="pr-page ws-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">项目工作台</h2>
        <p class="pr-subtitle">选中项目 → 拖入发布包 → 一键发布；发布历史支持回滚到之前上传的压缩包。</p>
      </div>
      <el-button type="primary" :icon="Plus" @click="openCreate">新建项目</el-button>
    </div>

    <div v-loading="loading" class="ws-grid">
      <!-- 项目列表 -->
      <aside class="col-list">
        <div class="pr-card list-card">
          <h3 class="card-title"><el-icon><Folder /></el-icon>工作项目</h3>
          <div class="proj-items">
            <div
              v-for="p in projects"
              :key="p.id"
              class="proj-item"
              :class="{ active: p.id === current?.id }"
              @click="selectProject(p)"
            >
              <span class="dot"></span>
              <span class="proj-name">{{ p.name }}</span>
              <span class="proj-ver pr-mono">{{ p.version }}</span>
            </div>
            <el-empty v-if="!projects.length" description="还没有工作项目" :image-size="70" />
          </div>
          <div class="list-foot">
            <span>共 {{ projects.length }} 个项目</span>
            <span>{{ isAdmin ? '全部项目可见' : '仅显示被授权项目' }}</span>
          </div>
        </div>
      </aside>

      <!-- 工作台 -->
      <section class="col-main" v-if="current">
        <div class="pr-card head-card">
          <div class="head-row">
            <span class="proj-title">{{ current.name }}</span>
            <el-tag effect="plain" class="code-tag">{{ current.code }}</el-tag>
            <el-tag type="primary" effect="plain" class="ver-tag pr-mono">{{ current.version }}</el-tag>
            <span class="head-right">
              <el-tag v-for="m in current.members" :key="m.id" effect="light" type="info" class="member-tag">
                {{ m.displayName || m.username }}
              </el-tag>
              <el-button :icon="Setting" @click="openSettings" :disabled="!canManage">项目设置</el-button>
            </span>
          </div>
          <div class="proj-desc" v-if="current.description">{{ current.description }}</div>
        </div>

        <div class="pr-card">
          <h3 class="card-title">更新目录</h3>
          <div class="card-sub">项目绑定的部署目标，发布时同包更新全部目录</div>
          <div class="dir-row" v-for="d in current.dirs" :key="d.id">
            <el-tag :type="envTag(d.serverEnv).type" size="small" effect="light">{{ envTag(d.serverEnv).label }}</el-tag>
            <div class="dir-main">
              <div class="dir-path pr-mono">{{ d.path }}</div>
              <div class="dir-meta">{{ d.serverName }} · {{ d.backupBeforeDeploy ? `发布前备份，保留 ${d.keepReleases} 版` : '不备份' }} · {{ d.allowOverwrite ? '允许覆盖' : '仅新增' }} · {{ d.packageMode === 'upload' ? '仅上传压缩包' : '自动解压' }}</div>
            </div>
          </div>
          <el-alert
            v-if="!current.dirs.length"
            type="info"
            :closable="false"
            title="尚未绑定更新目录"
            description="点击右上角「项目设置」，从已纳管服务器的目录中选择绑定。"
          />
        </div>

        <div class="pr-card deploy-card">
          <h3 class="card-title">部署空间</h3>
          <div class="card-sub">把发布包拖入下方区域，或从最近上传中选择</div>

          <el-upload drag :auto-upload="false" :show-file-list="false" :on-change="onFileChange" accept=".zip,.jar" class="uploader" :disabled="!current.dirs.length || dialog.publishing">
            <el-icon class="upload-icon"><UploadFilled /></el-icon>
            <div class="upload-text">拖拽 zip / jar 到此处，或点击选择文件</div>
            <div class="upload-hint">单包上限 200MB；发布时上传并自动校验 SHA256</div>
          </el-upload>

          <div class="pkg-list" v-if="packages.length">
            <div class="pkg-head">最近上传</div>
            <template v-for="g in recentGroups" :key="g.key">
              <div class="pkg-day" v-if="!g.isToday">{{ g.key }}</div>
              <div
                v-for="p in g.items"
                :key="p.id"
                class="pkg-item"
                :class="{ active: staged?.mode === 'pkg' && staged.pkg.id === p.id }"
                @click="pickRecent(p)"
              >
                <el-tag size="small" effect="plain">{{ p.kind.toUpperCase() }}</el-tag>
                <span class="pkg-name">{{ p.name }}</span>
                <span class="pkg-time">{{ formatDateTime(p.createdAt) }}</span>
                <span class="pkg-size">{{ formatSize(p.size) }}</span>
                <span class="pkg-hash pr-mono">{{ p.sha256.slice(0, 12) }}…</span>
              </div>
            </template>
          </div>

          <div class="staged" v-if="staged">
            <el-icon class="staged-icon"><UploadFilled /></el-icon>
            <div class="staged-main">
              <div class="staged-name">
                <span>{{ stagedText }}</span>
                <el-tag v-if="!uploadState.active" size="small" type="info" effect="light">待发布</el-tag>
              </div>
              <div class="staged-progress" v-if="uploadState.active">
                <el-progress
                  :percentage="uploadState.pct"
                  :stroke-width="6"
                  :show-text="false"
                  :status="uploadState.phase === 'publish' ? 'success' : undefined"
                />
                <span class="staged-pct">{{ uploadLabel }}</span>
              </div>
              <div class="staged-meta" v-else>
                {{
                  staged.mode === 'pkg'
                    ? `已选择历史安装包 · SHA256 ${staged.pkg.sha256.slice(0, 24)}…，发布时目标机再次校验`
                    : '待上传 · 点击发布后校验文件格式并登记 SHA256'
                }}
              </div>
            </div>
            <el-button link type="danger" :disabled="dialog.publishing" @click="clearStaged">移除</el-button>
          </div>

          <div class="script-pick">
            <div class="sp-item">
              <span class="sp-label">前置校验</span>
              <el-select v-model="publishPreScriptId" clearable size="small" placeholder="不执行">
                <el-option v-for="s in preScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
              </el-select>
            </div>
            <div class="sp-item">
              <span class="sp-label">部署后脚本</span>
              <el-select v-model="publishScriptId" clearable size="small" placeholder="不执行">
                <el-option v-for="s in postScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
              </el-select>
            </div>
          </div>
          <div class="script-hint">默认沿用项目设置中的部署后脚本，可在本次发布前调整或清空。</div>

          <div class="pub-bar">
            <span class="pub-summary">{{ dirSummary }}</span>
            <el-button type="primary" :disabled="!staged || !current.dirs.length" :loading="dialog.publishing" @click="clickPublish">发布项目</el-button>
          </div>

          <el-alert
            v-if="publishError"
            type="error"
            :closable="false"
            show-icon
            class="publish-alert"
            :title="`发布失败：${publishError}`"
          />

          <el-alert
            v-if="publishResult"
            type="success"
            :closable="true"
            show-icon
            class="publish-alert"
            @close="publishResult = null"
          >
            <template #title>发布任务已受理</template>
            <div class="publish-result-line">
              批次 <span class="pr-mono">{{ publishResult.batchNo }}</span> · 版本
              <span class="pr-mono">{{ publishResult.version }}</span> · 正在执行
            </div>
            <div class="publish-result-tip">进度见下方批次卡片或右侧「发布历史」，完成后会提示最终结果。</div>
          </el-alert>

          <!-- 批次执行进度 -->
          <div class="batch" v-if="activeBatch">
            <div class="batch-head">
              <span>{{ activeBatch.kind === 'rollback' ? '回滚任务' : '发布任务' }} {{ activeBatch.batchNo }} · {{ statusMeta[activeBatch.status]?.label || activeBatch.status }}</span>
              <span class="batch-ver pr-mono">{{ activeBatch.projectVersion }}</span>
            </div>
            <div class="batch-task" v-for="t in activeBatch.tasks" :key="t.id">
              <div class="bt-line">
                <el-tag :type="envTag(servers.find((s) => s.name === t.serverName)?.env || 'dev').type" size="small" effect="light">
                  {{ envTag(servers.find((s) => s.name === t.serverName)?.env || 'dev').label }}
                </el-tag>
                <span class="bt-path pr-mono">{{ t.targetPath }}</span>
                <el-tag size="small" :type="statusMeta[t.status]?.type || 'info'" effect="light">{{ statusMeta[t.status]?.label || t.status }}</el-tag>
                <span class="bt-pct">{{ t.progress }}%</span>
              </div>
              <el-progress :percentage="t.progress" :stroke-width="6" :show-text="false" />
              <div class="bt-msg" v-if="t.message && t.status !== 'success'">{{ t.message }}</div>
            </div>
          </div>
        </div>
      </section>

      <section class="col-main" v-else>
        <div class="pr-card"><el-empty description="选择或新建一个工作项目开始发布" /></div>
      </section>

      <!-- 发布历史 -->
      <aside class="col-hist">
        <div class="pr-card hist-card">
          <h3 class="card-title">发布历史</h3>
          <div class="card-sub" v-if="current">共 {{ history.length }} 次发布 · 点选记录可回滚</div>
          <div class="hist-hint">
            回滚 = 用该次上传的压缩包对项目全部目录重新发布，生成新任务并写入审计日志。
          </div>
          <div class="hist-rows">
            <div
              v-for="h in history"
              :key="h.batchNo"
              class="hist-row"
              :class="{ active: h.batchNo === selectedBatch }"
              @click="selectBatch(h)"
            >
              <div class="hist-line1">
                <span class="hist-ver pr-mono">{{ h.projectVersion }}</span>
                <el-tag v-if="h.kind === 'rollback'" size="small" type="primary" effect="light">回滚</el-tag>
                <el-tag size="small" :type="statusMeta[h.status]?.type || 'info'" effect="light">{{ statusMeta[h.status]?.label || h.status }}</el-tag>
                <span class="hist-time">{{ fmtTime(h.createdAt) }}</span>
              </div>
              <div class="hist-pkg pr-mono">{{ h.packageName }} · {{ formatSize(h.packageSize) }}</div>
              <div class="hist-meta">{{ h.operator }} · {{ h.tasks.length }} 个目录 · sha256 {{ h.sha256.slice(0, 10) }}…</div>
              <div class="hist-remark" v-if="h.remark">{{ h.remark }}</div>
            </div>
            <el-empty v-if="current && !history.length" description="暂无发布记录" :image-size="70" />
          </div>
          <div class="hist-foot" v-if="selectedHistory">
            <span>
              已选中 <b class="pr-mono">{{ selectedHistory.projectVersion }}</b> · {{ selectedHistory.packageName }}
              <template v-if="selectedHistory.status !== 'success'">（{{ statusMeta[selectedHistory.status]?.label }}）</template>
              <template v-else-if="selectedIsCurrent">（当前版本，无需回滚）</template>
            </span>
            <el-tooltip content="项目当前已处于该版本，回滚无意义" :disabled="!selectedIsCurrent" placement="top">
              <span>
                <el-button size="small" type="danger" plain :disabled="rollbackDisabled" @click="clickRollback">
                  回滚到此版本
                </el-button>
              </span>
            </el-tooltip>
          </div>
          <div class="hist-foot muted-foot" v-else>点选一条历史记录，可回滚到该次上传的压缩包</div>
        </div>
      </aside>
    </div>

    <!-- 新建项目 -->
    <el-dialog v-model="dialog.createVisible" title="新建工作项目" width="480px">
      <el-form label-width="92px">
        <el-form-item label="项目名称" required>
          <el-input v-model="dialog.createForm.name" placeholder="如：报表服务" />
        </el-form-item>
        <el-form-item label="项目标识">
          <el-input v-model="dialog.createForm.code" placeholder="如：report-export（小写字母/数字/连字符）" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="dialog.createForm.description" placeholder="一句话描述该项目" />
        </el-form-item>
        <el-form-item label="部署后脚本">
          <el-select v-model="dialog.createForm.scriptId" clearable placeholder="不执行部署后脚本" style="width: 100%">
            <el-option v-for="s in postScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
          </el-select>
          <div class="form-tip">发布时在全部目录更新完成后执行，之后可在项目设置中调整。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.createVisible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.saving" @click="saveCreate">创建项目</el-button>
      </template>
    </el-dialog>

    <!-- 项目设置 -->
    <el-dialog v-model="dialog.settingsVisible" :title="`项目设置 · ${current?.name || ''}`" width="640px">
      <el-form label-width="110px">
        <el-form-item label="绑定更新目录">
          <el-select v-model="dialog.settingsForm.directoryIds" multiple filterable style="width: 100%" placeholder="选择已纳管服务器的部署目录">
            <el-option v-for="d in allDirs" :key="d.id" :label="dirLabel(d)" :value="d.id" />
          </el-select>
          <div class="form-tip">发布时同一个包会更新全部绑定目录。</div>
        </el-form-item>
        <el-form-item label="部署后脚本">
          <el-select v-model="dialog.settingsForm.scriptId" clearable placeholder="不执行部署后脚本" style="width: 100%">
            <el-option v-for="s in postScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
          </el-select>
          <div class="form-tip">项目发布时统一执行该脚本；清空后发布只更新文件。</div>
        </el-form-item>
        <el-form-item label="项目成员" v-if="isAdmin">
          <el-select v-model="dialog.settingsForm.memberIds" multiple filterable style="width: 100%" placeholder="授权后成员可见并可操作本项目">
            <el-option v-for="u in users.filter((x) => x.role !== 'admin')" :key="u.id" :label="`${u.displayName || u.username}（${u.username}）`" :value="u.id" />
          </el-select>
          <div class="form-tip">成员即子用户的可见范围：仅被授权的用户能在工作台看到该项目。</div>
        </el-form-item>
        <el-form-item label="发布策略" v-if="isAdmin && dialog.settingsForm.directoryIds.length">
          <div class="policy">
            <el-switch v-model="dialog.settingsForm.policy.backupBeforeDeploy" active-text="发布前备份" />
            <el-switch v-model="dialog.settingsForm.policy.allowOverwrite" active-text="允许覆盖" />
            <span class="policy-keep">保留 <el-input-number v-model="dialog.settingsForm.policy.keepReleases" :min="1" :max="20" size="small" /> 版</span>
          </div>
          <div class="form-tip">策略将同步写入全部绑定目录（目录策略为全局配置）。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button v-if="canManage" type="danger" plain :icon="Delete" class="del-btn" @click="removeProject">删除项目</el-button>
        <el-button @click="dialog.settingsVisible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.saving" @click="saveSettings">保存设置</el-button>
      </template>
    </el-dialog>

    <!-- 发布确认（含生产 OTP） -->
    <el-dialog v-model="dialog.publishVisible" title="确认发布" width="520px">
      <el-alert type="warning" :closable="false" show-icon title="本次发布包含生产环境目录" class="dlg-alert" />
      <el-alert v-if="publishError" type="error" :closable="false" show-icon :title="publishError" class="dlg-alert" />
      <div class="dlg-info">
        项目：<b>{{ current?.name }}</b> · {{ current?.dirs.length }} 个更新目录<br />
        发布包：<span class="pr-mono">{{ stagedText }}</span><br />
        执行脚本：前置 <b>{{ publishPreScriptName || '不执行' }}</b> · 后置 <b>{{ publishScriptName || '不执行' }}</b>
      </div>
      <div class="dlg-progress" v-if="uploadState.active">
        <el-progress :percentage="uploadState.pct" :stroke-width="8" :show-text="false" :status="uploadState.phase === 'publish' ? 'success' : undefined" />
        <span class="dlg-progress-text">{{ uploadLabel }}</span>
      </div>
      <el-input v-model="dialog.otp" maxlength="6" placeholder="6 位动态口令" class="otp-input" />
      <template #footer>
        <el-button @click="dialog.publishVisible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.publishing" @click="doPublish">确认发布</el-button>
      </template>
    </el-dialog>

    <!-- 回滚确认（含生产 OTP） -->
    <el-dialog v-model="dialog.rollbackVisible" title="确认回滚" width="560px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        title="回滚将使用该次上传的历史压缩包，对项目绑定的全部目录重新执行一次部署（含生产环境），并生成新任务、写入审计日志。"
        class="dlg-alert"
      />
      <div class="dlg-info" v-if="selectedHistory">
        回滚目标：<b class="pr-mono">{{ selectedHistory.projectVersion }}</b> · <span class="pr-mono">{{ selectedHistory.packageName }}</span>（{{ formatSize(selectedHistory.packageSize) }}）<br />
        项目：<b>{{ current?.name }}</b> · {{ current?.dirs.length }} 个更新目录（{{ current?.dirs.map((d) => `${envTag(d.serverEnv).label}:${d.path}`).join('、') }}）
      </div>
      <el-input v-if="hasProd" v-model="dialog.otp" maxlength="6" placeholder="6 位动态口令" class="otp-input" />
      <template #footer>
        <el-button @click="dialog.rollbackVisible = false">取消</el-button>
        <el-button type="danger" :loading="dialog.rollbacking" @click="doRollback">确认回滚</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ws-page {
  min-height: calc(100vh - 104px);
}

.ws-grid {
  display: grid;
  grid-template-columns: 230px minmax(0, 1fr) 330px;
  grid-template-areas: 'list main hist';
  gap: 16px;
  align-items: stretch;
}

.col-list {
  grid-area: list;
  display: flex;
}

.col-list .pr-card {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.col-main {
  grid-area: main;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.col-main > .pr-card:last-child {
  flex: 1;
}

.col-hist {
  grid-area: hist;
  display: flex;
}

.col-hist .pr-card {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.card-title {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 4px;
}

.card-sub {
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 14px;
}

/* 项目列表 */
.proj-items {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.proj-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 10px 12px;
  border: 1px solid transparent;
  border-radius: 9px;
  cursor: pointer;
  font-size: 13px;
}

.proj-item:hover {
  background: #f4f6fb;
}

.proj-item.active {
  background: var(--pr-brand-soft);
  border-color: #c9d9ff;
}

.proj-item .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #c3cbd9;
  flex-shrink: 0;
}

.proj-item.active .dot {
  background: var(--pr-brand);
}

.proj-name {
  font-weight: 600;
}

.proj-ver {
  margin-left: auto;
  font-size: 11px;
  color: var(--pr-sub);
}

.list-foot {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  border-top: 1px dashed var(--pr-line);
  margin-top: 12px;
  padding-top: 10px;
  font-size: 11px;
  color: var(--pr-sub);
}

/* 项目头 */
.head-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.proj-title {
  font-size: 17px;
  font-weight: 650;
}

.code-tag,
.ver-tag {
  font-family: ui-monospace, Menlo, monospace;
}

.head-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.member-tag {
  font-size: 12px;
}

.proj-desc {
  margin-top: 8px;
  font-size: 12px;
  color: var(--pr-sub);
}

/* 目录 */
.dir-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--pr-line);
  border-radius: 10px;
  margin-bottom: 8px;
  background: #fbfcfe;
}

.dir-row:last-of-type {
  margin-bottom: 0;
}

.dir-main {
  min-width: 0;
}

.dir-path {
  font-size: 13px;
  font-weight: 600;
}

.dir-meta {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 3px;
}

/* 部署空间 */
.uploader :deep(.el-upload-dragger) {
  padding: 24px 16px;
  border-radius: 12px;
}

.upload-icon {
  font-size: 34px;
  color: #a9b6d6;
}

.upload-text {
  margin-top: 8px;
  font-size: 14px;
  color: #3d4658;
}

.upload-hint {
  margin-top: 5px;
  font-size: 12px;
  color: #9aa1ae;
}

.pkg-list {
  margin-top: 16px;
}

.pkg-head {
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 8px;
}

.pkg-day {
  margin: 10px 2px 4px;
  font-size: 12px;
  font-weight: 600;
  color: #9aa1ae;
  letter-spacing: 0.4px;
}

.pkg-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border-radius: 8px;
  cursor: pointer;
  border: 1px solid transparent;
}

.pkg-item:hover {
  background: #f7f9fd;
}

.pkg-item.active {
  background: var(--pr-brand-soft);
  border-color: #cfdcff;
}

.pkg-name {
  font-size: 13px;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pkg-size {
  font-size: 12px;
  color: var(--pr-sub);
}

.pkg-time {
  font-size: 12px;
  color: #9aa1ae;
  white-space: nowrap;
}

.pkg-hash {
  color: #9aa1ae;
}

.staged {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid #c9d9ff;
  background: var(--pr-brand-soft);
  border-radius: 10px;
}

.staged-icon {
  color: var(--pr-brand);
  font-size: 18px;
}

.staged-main {
  min-width: 0;
  flex: 1;
}

.staged-name {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 13px;
  font-weight: 600;
  word-break: break-all;
}

.staged-progress {
  margin-top: 7px;
}

.staged-pct {
  display: block;
  margin-top: 3px;
  font-size: 11px;
  color: var(--pr-brand);
}

.staged-meta {
  font-size: 11px;
  color: #5b6474;
  margin-top: 2px;
}

.publish-alert {
  margin-top: 12px;
  border-radius: 10px;
}

.publish-result-line {
  font-size: 13px;
  margin-top: 4px;
}

.publish-result-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 4px;
}

.script-pick {
  display: flex;
  gap: 12px;
  margin-top: 14px;
  flex-wrap: wrap;
}

.sp-item {
  flex: 1;
  min-width: 220px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.sp-label {
  font-size: 12px;
  color: var(--pr-sub);
  white-space: nowrap;
}

.sp-item :deep(.el-select) {
  flex: 1;
}

.script-hint {
  margin-top: 6px;
  font-size: 12px;
  color: #9aa1ae;
}

.pub-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-top: 14px;
  flex-wrap: wrap;
}

.pub-summary {
  font-size: 12px;
  color: var(--pr-sub);
}

/* 批次进度 */
.batch {
  margin-top: 14px;
  border: 1px solid #c9d9ff;
  background: #f7faff;
  border-radius: 10px;
  padding: 12px 14px;
}

.batch-head {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: #3d4a5c;
  margin-bottom: 10px;
}

.batch-ver {
  color: var(--pr-brand);
  font-weight: 600;
}

.batch-task {
  margin-bottom: 10px;
}

.batch-task:last-child {
  margin-bottom: 0;
}

.bt-line {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 5px;
  font-size: 12px;
}

.bt-path {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #4b5563;
}

.bt-pct {
  color: var(--pr-sub);
  font-variant-numeric: tabular-nums;
}

.bt-msg {
  font-size: 11px;
  color: #9aa1ae;
  margin-top: 3px;
}

/* 历史 */
.hist-hint {
  font-size: 12px;
  color: var(--pr-sub);
  background: #f4f6fb;
  border-radius: 8px;
  padding: 8px 10px;
  margin-bottom: 10px;
  line-height: 1.6;
}

.hist-rows {
  flex: 1;
}

.hist-row {
  padding: 10px 12px;
  border: 1px solid transparent;
  border-radius: 10px;
  cursor: pointer;
  margin-bottom: 4px;
}

.hist-row:hover {
  background: #f4f6fb;
}

.hist-row.active {
  background: var(--pr-brand-soft);
  border-color: #c9d9ff;
}

.hist-line1 {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.hist-ver {
  font-weight: 650;
  font-size: 13px;
}

.hist-time {
  margin-left: auto;
  font-size: 11px;
  color: #9aa1ae;
}

.hist-pkg {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 3px;
  word-break: break-all;
}

.hist-meta {
  font-size: 11px;
  color: #9aa1ae;
  margin-top: 3px;
}

.hist-remark {
  font-size: 11px;
  color: #b03030;
  margin-top: 3px;
}

.hist-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  border-top: 1px dashed var(--pr-line);
  padding-top: 12px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--pr-sub);
  flex-wrap: wrap;
}

.hist-foot b {
  color: var(--pr-text, #1f2430);
}

.muted-foot {
  display: block;
}

/* 弹窗 */
.form-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 4px;
  line-height: 1.6;
}

.policy {
  display: flex;
  gap: 18px;
  align-items: center;
  flex-wrap: wrap;
}

.policy-keep {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}

.del-btn {
  margin-right: auto;
}

.dlg-alert {
  border-radius: 10px;
  margin-bottom: 14px;
}

.dlg-info {
  font-size: 13px;
  line-height: 1.9;
  margin-bottom: 12px;
  word-break: break-all;
}

.dlg-progress {
  margin-bottom: 12px;
}

.dlg-progress-text {
  display: block;
  margin-top: 4px;
  font-size: 12px;
  color: var(--pr-sub);
}

.otp-input {
  max-width: 240px;
}

.otp-input :deep(input) {
  letter-spacing: 6px;
  font-family: ui-monospace, monospace;
}

@media (max-width: 1450px) {
  .ws-grid {
    grid-template-columns: 220px minmax(0, 1fr);
    grid-template-areas: 'list main' 'hist hist';
  }
}

@media (max-width: 900px) {
  .ws-grid {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas: 'list' 'main' 'hist';
  }
}
</style>
