<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { directoryApi, serverApi, type Directory, type HostKeyInfo, type Server, type ServerMetric } from '@/api'
import MetricRing from '@/components/MetricRing.vue'
import { useAuthStore } from '@/stores/auth'
import {
  envMeta,
  formatDateTime,
  formatUptime,
  loadPct,
  memPct,
  parseDirUsage,
  rootDiskPct,
  serverStatusMeta,
  usageColor,
  usageLevel,
} from '@/utils/status'

const auth = useAuthStore()
const list = ref<Server[]>([])
const loading = ref(false)

// ---------- 资源监控汇总 ----------

/** 在线服务器的最新指标快照 */
const onlineMetrics = computed(() =>
  list.value.map((s) => s.metrics).filter((m): m is ServerMetric => !!m && m.status === 'online'),
)
const onlineTotal = computed(() => list.value.filter((s) => s.status === 'online').length)
const offlineTotal = computed(() => list.value.filter((s) => s.status === 'offline').length)
const unknownTotal = computed(() => Math.max(0, list.value.length - onlineTotal.value - offlineTotal.value))
const onlineRate = computed(() =>
  list.value.length ? Math.round((onlineTotal.value / list.value.length) * 100) : null,
)
const totalCores = computed(() => onlineMetrics.value.reduce((sum, m) => sum + (m.cpuCores || 0), 0))

const memSummary = computed(() => {
  const values = onlineMetrics.value.map((m) => memPct(m)).filter((v): v is number => v != null)
  if (!values.length) return null
  return {
    avg: Math.round(values.reduce((a, b) => a + b, 0) / values.length),
    peak: Math.max(...values),
  }
})

const diskSummary = computed(() => {
  let peak: { path: string; pct: number } | null = null
  for (const m of onlineMetrics.value) {
    for (const u of parseDirUsage(m.dirUsage)) {
      if (!peak || u.pct > peak.pct) peak = { path: u.path, pct: Math.round(u.pct) }
    }
  }
  return peak
})

const loadSummary = computed(() => {
  const values = onlineMetrics.value.map((m) => loadPct(m)).filter((v): v is number => v != null)
  if (!values.length) return null
  return {
    avgLoad1: onlineMetrics.value.reduce((a, m) => a + m.load1, 0) / onlineMetrics.value.length,
    avgPct: Math.round(values.reduce((a, b) => a + b, 0) / values.length),
  }
})

/** 内存 >=90%、任一挂载点 >=90% 或负载超过核数，视为资源告警 */
const alertTotal = computed(
  () =>
    onlineMetrics.value.filter((m) => {
      const memHigh = (memPct(m) ?? 0) >= 90
      const diskHigh = parseDirUsage(m.dirUsage).some((u) => u.pct >= 90)
      const loadHigh = m.cpuCores > 0 && m.load1 > m.cpuCores
      return memHigh || diskHigh || loadHigh
    }).length,
)

function formatMB(mb: number) {
  if (!mb) return '0 MB'
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`
}

function dirsOf(m: ServerMetric) {
  return parseDirUsage(m.dirUsage)
}

function rootDiskCaption(m: ServerMetric) {
  const root = dirsOf(m)[0]
  return root ? `${formatMB(root.usedMB)} / ${formatMB(root.totalMB)}` : '无数据'
}

/** 1/5/15 分钟负载换算成相对核数的水位条 */
function loadWindows(m: ServerMetric) {
  const cores = m.cpuCores > 0 ? m.cpuCores : 1
  return [
    { label: '1 分钟', value: m.load1 },
    { label: '5 分钟', value: m.load5 },
    { label: '15 分钟', value: m.load15 },
  ].map((item) => {
    const pct = (item.value / cores) * 100
    return { ...item, width: item.value > 0 ? Math.max(2, Math.min(100, pct)) : 0, color: usageColor(pct) }
  })
}

const emptyServer = () => ({
  id: 0,
  name: '',
  env: 'test',
  host: '',
  port: 22,
  username: 'deploy',
  authType: 'key',
  credentialRef: 'file:~/.ssh/id_ed25519',
  credSource: 'ref' as 'ref' | 'inline',
  credentialSecret: '',
  maxConcurrent: 1,
  remark: '',
})

const dialog = reactive({ visible: false, saving: false, form: emptyServer() })

const drawer = reactive({
  visible: false,
  server: null as Server | null,
  dirs: [] as Directory[],
  saving: false,
  form: {
    id: 0,
    path: '',
    owner: 'app',
    allowOverwrite: true,
    backupBeforeDeploy: true,
    cleanReplace: false,
    packageMode: 'unpack',
    keepReleases: 3,
    maxSizeMB: 512,
    remark: '',
  },
})

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    list.value = await serverApi.list()
  } catch (e: any) {
    if (!silent) ElMessage.error(e.message)
  } finally {
    if (!silent) loading.value = false
  }
}

// ---------- 自动刷新 ----------

const autoRefresh = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null

watch(autoRefresh, (on) => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  if (on) refreshTimer = setInterval(() => load(true), 30_000)
})

onBeforeUnmount(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})

function openCreate() {
  dialog.form = emptyServer()
  dialog.visible = true
}

function openEdit(row: Server) {
  dialog.form = { ...emptyServer(), ...row }
  // 内置凭据（secret:）自动切到直接录入形态，不回显密文
  if (row.credentialRef?.startsWith('secret:')) {
    dialog.form.credSource = 'inline'
    dialog.form.credentialRef = 'secret:内置凭据'
    dialog.form.credentialSecret = ''
  }
  dialog.visible = true
}

async function save() {
  const form = dialog.form
  if (!form.name || !form.host) {
    ElMessage.warning('请填写名称与主机地址')
    return
  }
  // 组装提交体：credSource 是纯前端状态；credentialSecret 仅在直接录入且有内容时提交
  const payload: Record<string, unknown> = {
    id: form.id, name: form.name, env: form.env, host: form.host, port: form.port,
    username: form.username, authType: form.authType,
    maxConcurrent: form.maxConcurrent, remark: form.remark,
  }
  if (form.credSource === 'inline') {
    if (form.credentialSecret) {
      payload.credentialSecret = form.credentialSecret
    } else if (form.id) {
      payload.credentialRef = 'secret:内置凭据' // 编辑时留空 = 保持内置不变
    } else {
      ElMessage.warning('请填写凭据内容（密码或私钥）')
      return
    }
  } else {
    payload.credentialRef = form.credentialRef
  }
  dialog.saving = true
  try {
    if (form.id) {
      await serverApi.update(form.id, payload)
    } else {
      await serverApi.create(payload)
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

async function probe(row: Server) {
  const loadingMsg = ElMessage({ message: `正在检测 ${row.name}（连接并采集运行指标）…`, type: 'info', duration: 0 })
  try {
    const res = await serverApi.probe(row.id)
    if (res.status === 'online') {
      const cred = res.credential?.message ? `，凭据${res.credential.message}` : ''
      ElMessage.success(`在线（${res.mode === 'local' ? '本地沙箱' : 'SSH'}）${cred}`)
    } else {
      const cred = res.credential && !res.credential.ok ? `；凭据自检：${res.credential.message}` : ''
      ElMessage.error(res.message + cred)
    }
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loadingMsg.close()
  }
}

// ---------- 主机指纹 ----------

const hk = reactive({
  visible: false,
  server: null as Server | null,
  probing: false,
  clearing: false,
  info: null as HostKeyInfo | null,
})

function openHostKey(row: Server) {
  hk.server = row
  hk.info = null
  hk.visible = true
}

async function probeHostKey() {
  if (!hk.server) return
  hk.probing = true
  try {
    hk.info = await serverApi.probeHostKey(hk.server.id)
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    hk.probing = false
  }
}

async function clearHostKey() {
  if (!hk.server) return
  try {
    await ElMessageBox.confirm(
      `确认清除「${hk.server.name}」已登记的主机指纹？清除后下次连接将重新自动信任，仅在服务器重建后使用。`,
      '清除指纹确认',
      { type: 'warning' },
    )
  } catch {
    return
  }
  hk.clearing = true
  try {
    await serverApi.clearHostKey(hk.server.id)
    ElMessage.success('已清除，下次连接将重新自动信任')
    hk.server = { ...hk.server, hostKeyAlgo: '', hostKeyFingerprint: '', hostKeyPinnedAt: undefined }
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    hk.clearing = false
  }
}

function shortFp(fp?: string) {
  return fp ? fp.replace('SHA256:', '').slice(0, 12) + '…' : ''
}

async function remove(row: Server) {
  try {
    await ElMessageBox.confirm(`确认删除服务器「${row.name}」及其目录配置？`, '删除确认', { type: 'warning' })
    await serverApi.remove(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(e.message || '删除失败')
  }
}

async function openDirs(row: Server) {
  drawer.server = row
  drawer.visible = true
  drawer.form = { id: 0, path: '', owner: 'app', allowOverwrite: true, backupBeforeDeploy: true, cleanReplace: false, packageMode: 'unpack', keepReleases: 3, maxSizeMB: 512, remark: '' }
  await loadDirs()
}

async function loadDirs() {
  if (!drawer.server) return
  try {
    drawer.dirs = await directoryApi.list(drawer.server.id)
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

function editDir(d: Directory) {
  drawer.form = { ...d, cleanReplace: !!d.cleanReplace, packageMode: d.packageMode || 'unpack' }
}

function resetDir() {
  drawer.form = { id: 0, path: '', owner: 'app', allowOverwrite: true, backupBeforeDeploy: true, cleanReplace: false, packageMode: 'unpack', keepReleases: 3, maxSizeMB: 512, remark: '' }
}

async function saveDir() {
  if (!drawer.server) return
  if (!drawer.form.path.startsWith('/')) {
    ElMessage.warning('部署目录必须是绝对路径')
    return
  }
  drawer.saving = true
  try {
    if (drawer.form.id) {
      await directoryApi.update(drawer.form.id, { ...drawer.form, serverId: drawer.server.id })
    } else {
      await directoryApi.create({ ...drawer.form, serverId: drawer.server.id })
    }
    ElMessage.success('已保存')
    resetDir()
    await loadDirs()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    drawer.saving = false
  }
}

async function removeDir(d: Directory) {
  try {
    await ElMessageBox.confirm(`确认删除目录配置「${d.path}」？`, '删除确认', { type: 'warning' })
    await directoryApi.remove(d.id)
    await loadDirs()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(e.message || '删除失败')
  }
}

onMounted(() => load())
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">服务器</h2>
        <p class="pr-subtitle">中控机通过执行通道纳管业务服务器，凭据以引用方式保存。</p>
      </div>
      <div class="actions">
        <span class="auto-label">自动刷新</span>
        <el-switch v-model="autoRefresh" size="small" />
        <el-button :icon="Refresh" @click="load()">刷新</el-button>
        <el-button v-if="auth.isAdmin" type="primary" :icon="Plus" @click="openCreate">新增服务器</el-button>
      </div>
    </div>

    <div class="monitor-grid">
      <div class="monitor-card">
        <div class="mc-head">
          <span class="mc-label">服务器在线率</span>
          <span class="mc-sub">{{ list.length }} 台纳管</span>
        </div>
        <div class="mc-online">
          <!-- 在线率是「越高越好」的指标，固定绿色，与「在线」圆点同色 -->
          <MetricRing :percentage="onlineRate" :size="78" :stroke="8" color="#34c759" />
          <div class="mc-legend">
            <div><i class="dot online" />在线 <b>{{ onlineTotal }}</b></div>
            <div><i class="dot offline" />不可达 <b>{{ offlineTotal }}</b></div>
            <div><i class="dot unknown" />未检测 <b>{{ unknownTotal }}</b></div>
          </div>
        </div>
      </div>

      <div class="monitor-card">
        <div class="mc-head">
          <span class="mc-label">内存水位</span>
          <span class="mc-sub">{{ onlineMetrics.length }} 台采样</span>
        </div>
        <div class="mc-value" :style="{ color: usageColor(memSummary?.avg) }">
          {{ memSummary ? memSummary.avg : '—' }}<em>%</em>
        </div>
        <div class="mc-bar">
          <span :style="{ width: `${memSummary?.avg ?? 0}%`, background: usageColor(memSummary?.avg) }" />
        </div>
        <div class="mc-foot">{{ memSummary ? `峰值 ${memSummary.peak}% · 告警阈值 90%` : '暂无在线服务器指标' }}</div>
      </div>

      <div class="monitor-card">
        <div class="mc-head">
          <span class="mc-label">磁盘水位</span>
          <span class="mc-sub">全部挂载点</span>
        </div>
        <div class="mc-value" :style="{ color: usageColor(diskSummary?.pct) }">
          {{ diskSummary ? diskSummary.pct : '—' }}<em>%</em>
        </div>
        <div class="mc-bar">
          <span :style="{ width: `${diskSummary?.pct ?? 0}%`, background: usageColor(diskSummary?.pct) }" />
        </div>
        <div class="mc-foot" :title="diskSummary?.path">
          {{ diskSummary ? `最高占用 ${diskSummary.path}` : '暂无磁盘采集数据' }}
        </div>
      </div>

      <div class="monitor-card">
        <div class="mc-head">
          <span class="mc-label">CPU 负载</span>
          <el-tag v-if="alertTotal > 0" type="danger" size="small" effect="light">{{ alertTotal }} 台告警</el-tag>
          <el-tag v-else-if="onlineMetrics.length" type="success" size="small" effect="light">运行平稳</el-tag>
          <span v-else class="mc-sub">无采样</span>
        </div>
        <div class="mc-value" :style="{ color: usageColor(loadSummary?.avgPct) }">
          {{ loadSummary ? loadSummary.avgLoad1.toFixed(2) : '—' }}
        </div>
        <div class="mc-bar">
          <span
            :style="{
              width: `${Math.min(100, loadSummary?.avgPct ?? 0)}%`,
              background: usageColor(loadSummary?.avgPct),
            }"
          />
        </div>
        <div class="mc-foot">
          {{ onlineMetrics.length ? `1 分钟均值 · ${totalCores} 核 · 水位 ${loadSummary?.avgPct ?? 0}%` : '暂无在线服务器指标' }}
        </div>
      </div>
    </div>

    <div class="pr-card">
      <el-table v-loading="loading" :data="list" style="width: 100%">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div v-if="row.metrics" class="metric-detail">
              <template v-if="row.metrics.status === 'online'">
                <div class="md-head">
                  <div class="md-ident">
                    <span class="status-dot" :class="row.status" />
                    <span class="md-host">{{ row.metrics.hostname || row.name }}</span>
                    <el-tag size="small" effect="plain">{{ row.metrics.os || '系统未知' }}</el-tag>
                  </div>
                  <div class="md-meta">
                    <span>运行 {{ formatUptime(row.metrics.uptimeSec) }}</span>
                    <span>采集于 {{ formatDateTime(row.metrics.probedAt) }}</span>
                    <span>{{ row.metrics.probeMode === 'local' ? '本地沙箱' : 'SSH 采集' }}</span>
                  </div>
                </div>

                <div class="md-body">
                  <MetricRing
                    :percentage="memPct(row.metrics)"
                    label="内存"
                    :caption="`${formatMB(row.metrics.memUsedMB)} / ${formatMB(row.metrics.memTotalMB)}`"
                  />
                  <MetricRing
                    :percentage="rootDiskPct(row.metrics)"
                    label="根分区磁盘"
                    :caption="rootDiskCaption(row.metrics)"
                  />
                  <MetricRing
                    :percentage="loadPct(row.metrics)"
                    :display="row.metrics.cpuCores ? row.metrics.load1.toFixed(2) : '—'"
                    unit=""
                    label="CPU 负载"
                    :caption="`${row.metrics.cpuCores || '—'} 核 · 1 分钟`"
                  />
                  <div class="md-load-panel">
                    <div class="md-load-title">负载窗口</div>
                    <div v-for="item in loadWindows(row.metrics)" :key="item.label" class="load-row">
                      <span class="load-label">{{ item.label }}</span>
                      <div class="load-track">
                        <span class="load-fill" :style="{ width: `${item.width}%`, background: item.color }" />
                      </div>
                      <span class="load-num">{{ item.value.toFixed(2) }}</span>
                    </div>
                    <div class="md-load-foot">负载达到 {{ row.metrics.cpuCores || 1 }} 时 CPU 满载</div>
                  </div>
                </div>

                <div class="md-disks">
                  <div class="md-section-title">目录磁盘占用</div>
                  <div v-for="u in dirsOf(row.metrics)" :key="u.path" class="disk-row">
                    <span class="disk-path pr-mono" :title="u.path">{{ u.path }}</span>
                    <div class="disk-track">
                      <span
                        class="disk-fill"
                        :style="{ width: `${Math.min(100, u.pct)}%`, background: usageColor(u.pct) }"
                      />
                    </div>
                    <span class="disk-pct" :class="`lv-${usageLevel(u.pct)}`">{{ Math.round(u.pct) }}%</span>
                    <span class="disk-size">{{ formatMB(u.usedMB) }} / {{ formatMB(u.totalMB) }}</span>
                  </div>
                  <div v-if="!dirsOf(row.metrics).length" class="md-none">暂无磁盘数据</div>
                </div>
              </template>
              <el-alert v-else type="error" :closable="false" :title="row.metrics.message || '采集失败'" />
            </div>
            <el-empty v-else description="暂无采集数据，点击「检测」立即采集；配置后台采集间隔后自动更新" :image-size="60" />
          </template>
        </el-table-column>
        <el-table-column label="服务器" min-width="185">
          <template #default="{ row }">
            <div class="srv">
              <span class="name">{{ row.name }}</span>
              <el-tag :type="envMeta(row.env).type" size="small" effect="light">{{ envMeta(row.env).label }}</el-tag>
            </div>
            <div class="addr pr-mono">{{ row.username }}@{{ row.host }}:{{ row.port }}</div>
          </template>
        </el-table-column>
        <el-table-column label="认证" width="175">
          <template #default="{ row }">
            <div class="auth">{{ row.authType === 'key' ? '密钥' : '口令' }}</div>
            <div class="ref pr-mono" :title="row.credentialRef">{{ row.credentialRef }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="maxConcurrent" label="并发" width="60" />
        <el-table-column label="运行状态" width="190">
          <template #default="{ row }">
            <div class="status-cell">
              <span class="status-dot" :class="row.status" />
              <el-tag :type="serverStatusMeta(row.status).type" size="small">
                {{ serverStatusMeta(row.status).label }}
              </el-tag>
            </div>
            <div v-if="row.metrics?.status === 'online'" class="metric-line" :title="`负载 ${row.metrics.load1.toFixed(2)} · 内存 ${memPct(row.metrics) ?? '—'}% · 磁盘 ${rootDiskPct(row.metrics) ?? '—'}%`">
              <span :class="`lv-${usageLevel(loadPct(row.metrics))}`">负载 {{ row.metrics.load1.toFixed(2) }}</span>
              <span class="sep">·</span>
              <span :class="`lv-${usageLevel(memPct(row.metrics))}`">内存 {{ memPct(row.metrics) ?? '—' }}%</span>
              <span class="sep">·</span>
              <span :class="`lv-${usageLevel(rootDiskPct(row.metrics))}`">磁盘 {{ rootDiskPct(row.metrics) ?? '—' }}%</span>
            </div>
            <div v-else-if="row.metrics && row.metrics.status === 'offline'" class="metric-line fail" :title="row.metrics.message">
              {{ row.metrics.message }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="指纹" width="130">
          <template #default="{ row }">
            <template v-if="row.hostKeyFingerprint">
              <el-tooltip :content="row.hostKeyFingerprint" placement="top">
                <span class="fp pr-mono">{{ shortFp(row.hostKeyFingerprint) }}</span>
              </el-tooltip>
              <div class="fp-sub">已登记（{{ row.hostKeyAlgo }}）</div>
            </template>
            <span v-else class="fp-sub">未登记 · 首次连接自动信任</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="80" show-overflow-tooltip />
        <el-table-column label="操作" width="250">
          <template #default="{ row }">
            <el-button link type="primary" @click="probe(row)">检测</el-button>
            <el-button link type="primary" @click="openHostKey(row)">指纹</el-button>
            <el-button link type="primary" @click="openDirs(row)">目录</el-button>
            <el-button v-if="auth.isAdmin" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-if="auth.isAdmin" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="dialog.visible" :title="dialog.form.id ? '编辑服务器' : '新增服务器'" width="560px">
      <el-form label-width="92px">
        <el-form-item label="名称" required>
          <el-input v-model="dialog.form.name" placeholder="app-prod-01" />
        </el-form-item>
        <el-form-item label="环境">
          <el-radio-group v-model="dialog.form.env">
            <el-radio-button value="dev">开发</el-radio-button>
            <el-radio-button value="test">测试</el-radio-button>
            <el-radio-button value="prod">生产</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="主机地址" required>
          <div class="host-row">
            <el-input v-model="dialog.form.host" placeholder="10.0.10.11" />
            <el-input-number v-model="dialog.form.port" :min="1" :max="65535" controls-position="right" />
          </div>
        </el-form-item>
        <el-form-item label="登录用户">
          <el-input v-model="dialog.form.username" placeholder="deploy" />
        </el-form-item>
        <el-form-item label="认证方式">
          <el-select v-model="dialog.form.authType" style="width: 100%">
            <el-option label="私钥" value="key" />
            <el-option label="口令" value="password" />
          </el-select>
        </el-form-item>
        <el-form-item label="凭据来源">
          <el-radio-group v-model="dialog.form.credSource">
            <el-radio-button value="ref">引用（env:/file:）</el-radio-button>
            <el-radio-button value="inline">直接录入（加密保存）</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="dialog.form.credSource === 'ref'" label="凭据引用">
          <el-input v-model="dialog.form.credentialRef" placeholder="file:~/.ssh/id_ed25519 或 env:PROD_SSH_PASSWORD" />
          <div class="form-tip">只填写引用，不填写明文；运行时由中控机解析（需中控机上存在该文件/环境变量）。</div>
        </el-form-item>
        <el-form-item v-else :label="dialog.form.authType === 'key' ? '私钥内容' : '登录密码'">
          <el-input
            v-if="dialog.form.authType === 'key'"
            v-model="dialog.form.credentialSecret"
            type="textarea"
            :rows="5"
            placeholder="粘贴 OpenSSH 私钥全文（不支持带 passphrase）"
          />
          <el-input
            v-else
            v-model="dialog.form.credentialSecret"
            type="password"
            show-password
            placeholder="服务器登录密码"
          />
          <div class="form-tip">
            凭据经 AES-256-GCM 加密后入库（主密钥由 security.credential_key / 环境变量
            PACKRUN_CREDENTIAL_KEY 提供，不落库）；数据库与主密钥分离存放。
            <template v-if="dialog.form.id">留空表示保持现有内置凭据不变。</template>
          </div>
        </el-form-item>
        <el-form-item label="并发上限">
          <el-input-number v-model="dialog.form.maxConcurrent" :min="1" :max="10" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="dialog.form.remark" placeholder="生产应用节点" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="hk.visible" :title="`主机指纹 · ${hk.server?.name || ''}`" width="560px">
      <el-alert
        type="info"
        :closable="false"
        class="hk-tip"
        title="指纹即目标机 SSH 主机公钥的 SHA256 摘要。首次连接成功时自动登记（TOFU），此后每次连接自动校验，不匹配即拒绝连接——这是防中间人攻击的关键。"
      />
      <div class="hk-current">
        <template v-if="hk.server?.hostKeyFingerprint">
          <div class="hk-row">
            <span class="hk-label">登记状态</span>
            <el-tag type="success" size="small">已登记</el-tag>
            <span class="hk-sub">登记于 {{ hk.server.hostKeyPinnedAt ? new Date(hk.server.hostKeyPinnedAt).toLocaleString('zh-CN', { hour12: false }) : '—' }}</span>
          </div>
          <div class="hk-row">
            <span class="hk-label">登记指纹</span>
            <span class="pr-mono hk-fp">{{ hk.server.hostKeyFingerprint }}</span>
          </div>
          <div class="hk-row">
            <span class="hk-label">公钥算法</span>
            <span>{{ hk.server.hostKeyAlgo || '—' }}</span>
          </div>
        </template>
        <template v-else>
          <div class="hk-row">
            <span class="hk-label">登记状态</span>
            <el-tag type="info" size="small">未登记</el-tag>
            <span class="hk-sub">首次成功连接（检测或部署）时自动登记</span>
          </div>
        </template>
      </div>

      <template v-if="hk.info">
        <el-divider content-position="left">本次采集结果</el-divider>
        <el-alert
          v-if="hk.info.pinned && !hk.info.matched"
          type="error"
          :closable="false"
          title="指纹不匹配！"
          description="目标机当前指纹与登记值不一致：服务器可能已重建，也可能存在中间人风险。若确认服务器已重建，请清除指纹后重新信任。"
        />
        <div v-else class="hk-current">
          <div class="hk-row">
            <span class="hk-label">当前指纹</span>
            <span class="pr-mono hk-fp">{{ hk.info.fingerprint }}</span>
          </div>
          <div class="hk-row">
            <span class="hk-label">公钥算法</span>
            <span>{{ hk.info.algo }}</span>
            <el-tag v-if="hk.info.pinned && hk.info.matched" type="success" size="small">与登记一致</el-tag>
            <el-tag v-if="!hk.info.pinned" type="info" size="small">尚未登记</el-tag>
          </div>
        </div>
      </template>

      <template #footer>
        <el-button v-if="auth.isAdmin && hk.server?.hostKeyFingerprint" type="danger" plain :loading="hk.clearing" @click="clearHostKey">
          清除指纹
        </el-button>
        <el-button :loading="hk.probing" @click="probeHostKey">采集当前指纹</el-button>
        <el-button type="primary" @click="hk.visible = false">关闭</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="drawer.visible" :title="`部署目录 · ${drawer.server?.name || ''}`" size="640px">
      <el-table :data="drawer.dirs" size="small">
        <el-table-column prop="path" label="目录" min-width="200">
          <template #default="{ row }">
            <span class="pr-mono">{{ row.path }}</span>
          </template>
        </el-table-column>
        <el-table-column label="策略" width="180">
          <template #default="{ row }">
            <div class="policy">
              <span>{{ row.allowOverwrite ? '可覆盖' : '仅新增' }}</span>
              <span>{{ row.backupBeforeDeploy ? '部署前备份' : '不备份' }}</span>
              <span v-if="row.cleanReplace">干净替换</span>
              <span>{{ row.packageMode === 'upload' ? '仅上传压缩包' : '自动解压' }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template #default="{ row }">
            <el-button link type="primary" @click="editDir(row)">编辑</el-button>
            <el-button link type="danger" @click="removeDir(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-divider content-position="left">{{ drawer.form.id ? '编辑目录' : '新增目录' }}</el-divider>

      <el-form label-width="96px" size="default">
        <el-form-item label="绝对路径" required>
          <el-input v-model="drawer.form.path" placeholder="/opt/apps/order-service" />
        </el-form-item>
        <el-form-item label="属主">
          <el-input v-model="drawer.form.owner" placeholder="app" />
        </el-form-item>
        <el-form-item label="覆盖策略">
          <el-switch v-model="drawer.form.allowOverwrite" active-text="允许覆盖" inactive-text="仅新增" />
        </el-form-item>
        <el-form-item label="备份策略">
          <el-switch v-model="drawer.form.backupBeforeDeploy" active-text="部署前备份" inactive-text="不备份" />
        </el-form-item>
        <el-form-item label="干净替换">
          <el-switch v-model="drawer.form.cleanReplace" active-text="开启" inactive-text="关闭" />
          <div class="form-tip">开启后 zip 自动解压时清理包内不存在的旧文件（仅限部署目录内），实现完整替换发布。</div>
        </el-form-item>
        <el-form-item label="包处理">
          <el-radio-group v-model="drawer.form.packageMode">
            <el-radio-button value="unpack">自动解压</el-radio-button>
            <el-radio-button value="upload">仅上传</el-radio-button>
          </el-radio-group>
          <div class="form-tip">仅对 zip 生效：自动解压直接展开到目录；仅上传保留压缩包，由部署后脚本处理。</div>
        </el-form-item>
        <el-form-item label="保留版本">
          <el-input-number v-model="drawer.form.keepReleases" :min="0" :max="20" />
        </el-form-item>
        <el-form-item label="空间阈值">
          <el-input-number v-model="drawer.form.maxSizeMB" :min="16" :max="10240" />
          <span class="unit">MB</span>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="drawer.saving" @click="saveDir">保存目录</el-button>
          <el-button v-if="drawer.form.id" @click="resetDir">取消编辑</el-button>
        </el-form-item>
      </el-form>
    </el-drawer>
  </div>
</template>

<style scoped>
.actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.auto-label {
  font-size: 12px;
  color: var(--pr-sub);
}

/* ---------- 顶部监控卡片 ---------- */

.monitor-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
  margin-bottom: 18px;
}

.monitor-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 18px;
  background: var(--pr-panel);
  border: 1px solid var(--pr-line);
  border-radius: 12px;
}

.mc-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 22px;
}

.mc-label {
  font-size: 12px;
  color: var(--pr-sub);
}

.mc-sub {
  font-size: 11px;
  color: #9aa1ae;
}

.mc-online {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 18px;
}

.mc-legend {
  display: grid;
  gap: 7px;
  font-size: 12px;
  color: var(--pr-sub);
}

.mc-legend b {
  margin-left: 2px;
  color: var(--pr-text);
  font-weight: 600;
}

.dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  margin-right: 6px;
  border-radius: 50%;
  vertical-align: 1px;
}

.dot.online {
  background: #34c759;
}

.dot.offline {
  background: var(--pr-danger);
}

.dot.unknown {
  background: #c3c9d6;
}

.mc-value {
  font-size: 26px;
  font-weight: 650;
  line-height: 1.1;
  letter-spacing: 0.3px;
}

.mc-value em {
  margin-left: 2px;
  font-size: 12px;
  font-style: normal;
  font-weight: 500;
  color: var(--pr-sub);
}

.mc-bar {
  height: 6px;
  border-radius: 99px;
  background: #eef1f7;
  overflow: hidden;
}

.mc-bar span {
  display: block;
  height: 100%;
  border-radius: 99px;
  transition: width 0.5s ease, background 0.3s ease;
}

.mc-foot {
  font-size: 11px;
  color: #9aa1ae;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ---------- 表格里的状态 ---------- */

.srv {
  display: flex;
  align-items: center;
  gap: 8px;
}

.name {
  font-weight: 600;
}

.addr,
.ref {
  color: var(--pr-sub);
  margin-top: 3px;
}

.ref {
  max-width: 148px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.auth {
  font-size: 13px;
}

.host-row {
  display: flex;
  gap: 10px;
  width: 100%;
}

.form-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 4px;
  line-height: 1.5;
}

.policy {
  display: flex;
  flex-direction: column;
  font-size: 12px;
  color: var(--pr-sub);
  gap: 2px;
}

.unit {
  margin-left: 8px;
  color: var(--pr-sub);
  font-size: 12px;
}

.status-cell {
  display: flex;
  align-items: center;
  gap: 7px;
}

.status-dot {
  flex-shrink: 0;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #c3c9d6;
}

.status-dot.online {
  background: #34c759;
  animation: pr-pulse 2s ease-out infinite;
}

.status-dot.offline {
  background: var(--pr-danger);
}

@keyframes pr-pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(52, 199, 89, 0.35);
  }
  70% {
    box-shadow: 0 0 0 6px rgba(52, 199, 89, 0);
  }
  100% {
    box-shadow: 0 0 0 0 rgba(52, 199, 89, 0);
  }
}

.metric-line {
  margin-top: 4px;
  font-size: 12px;
  color: var(--pr-sub);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.metric-line .sep {
  margin: 0 4px;
  color: #c3c9d6;
}

.metric-line.fail {
  color: var(--el-color-danger);
}

.lv-warn {
  color: #e6a23c;
  font-weight: 600;
}

.lv-danger {
  color: var(--pr-danger);
  font-weight: 600;
}

.fp {
  font-size: 12px;
}

.fp-sub {
  margin-top: 3px;
  font-size: 12px;
  color: var(--pr-sub);
}

/* ---------- 展开后的指标面板 ---------- */

.metric-detail {
  margin: 2px 16px 16px;
  padding: 16px 18px;
  background: linear-gradient(180deg, #f8faff 0%, #fcfdff 100%);
  border: 1px solid #e9edf7;
  border-radius: 10px;
}

.md-head {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}

.md-ident {
  display: flex;
  align-items: center;
  gap: 8px;
}

.md-host {
  font-size: 14px;
  font-weight: 650;
}

.md-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 14px;
  font-size: 12px;
  color: var(--pr-sub);
}

.md-body {
  display: grid;
  grid-template-columns: repeat(3, 118px) minmax(220px, 1fr);
  gap: 14px 18px;
  align-items: stretch;
}

.md-load-panel {
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 12px 16px;
  background: #fff;
  border: 1px solid #e9edf7;
  border-radius: 10px;
}

.md-load-title {
  font-size: 12px;
  font-weight: 600;
  color: #4b5563;
}

.load-row {
  display: grid;
  grid-template-columns: 46px minmax(80px, 1fr) 44px;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--pr-sub);
}

.load-track {
  height: 6px;
  border-radius: 99px;
  background: #eef1f7;
  overflow: hidden;
}

.load-fill {
  display: block;
  height: 100%;
  border-radius: 99px;
  transition: width 0.5s ease, background 0.3s ease;
}

.load-num {
  text-align: right;
  color: var(--pr-text);
  font-variant-numeric: tabular-nums;
}

.md-load-foot {
  margin-top: 10px;
  font-size: 11px;
  color: #9aa1ae;
}

.md-disks {
  display: grid;
  gap: 9px;
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px dashed #e3e8f4;
}

.md-section-title {
  font-size: 12px;
  font-weight: 600;
  color: #4b5563;
}

.disk-row {
  display: grid;
  grid-template-columns: minmax(150px, 280px) minmax(120px, 1fr) 48px 130px;
  align-items: center;
  gap: 12px;
  font-size: 12px;
}

.disk-path {
  color: #4b5563;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.disk-track {
  height: 8px;
  border-radius: 99px;
  background: #eef1f7;
  overflow: hidden;
}

.disk-fill {
  display: block;
  height: 100%;
  border-radius: 99px;
  transition: width 0.5s ease, background 0.3s ease;
}

.disk-pct {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.disk-size {
  text-align: right;
  color: var(--pr-sub);
  font-variant-numeric: tabular-nums;
}

.md-none {
  font-size: 12px;
  color: #9aa1ae;
}

@media (max-width: 1180px) {
  .monitor-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .md-body {
    grid-template-columns: repeat(3, 110px) minmax(180px, 1fr);
  }
}

@media (max-width: 900px) {
  .md-body {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.hk-tip {
  margin-bottom: 14px;
}

.hk-current {
  display: grid;
  gap: 10px;
}

.hk-row {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
}

.hk-label {
  color: var(--pr-sub);
  width: 68px;
  flex-shrink: 0;
}

.hk-sub {
  color: var(--pr-sub);
  font-size: 12px;
}

.hk-fp {
  word-break: break-all;
}
</style>
