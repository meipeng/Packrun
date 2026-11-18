<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { overviewApi, type Overview } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { envMeta, memPct, parseDirUsage, rootDiskPct, serverStatusMeta } from '@/utils/status'

const router = useRouter()
const auth = useAuthStore()
const data = ref<Overview | null>(null)
const loading = ref(true)
const refreshing = ref(false)
let timer: number | undefined

const health = computed(() => data.value?.health)
const healthy = computed(() => health.value?.status === 'ok')

const checkRows = computed(() => {
  const meta: Record<string, { name: string; hint: string }> = {
    database: { name: '数据库', hint: '连接可用、完整性检查、库目录可写' },
    packageDir: { name: '包仓库', hint: '目录可写与配额占用' },
    disk: { name: '磁盘空间', hint: '包仓库所在文件系统' },
    tasks: { name: '近期任务', hint: '最近 1 小时的失败任务数' },
  }
  return Object.entries(health.value?.checks || {}).map(([key, check]) => ({
    key,
    name: meta[key]?.name || key,
    hint: meta[key]?.hint || '',
    ...check,
  }))
})

const pkgUsed = computed(() => health.value?.checks.packageDir?.metrics?.usedMB ?? 0)
const pkgMax = computed(() => health.value?.checks.packageDir?.metrics?.maxMB ?? 0)
const pkgPct = computed(() => health.value?.checks.packageDir?.metrics?.pct ?? 0)
const diskFree = computed(() => health.value?.checks.disk?.metrics?.freeMB ?? 0)
const diskTotal = computed(() => health.value?.checks.disk?.metrics?.totalMB ?? 0)
const diskUsedPct = computed(() => health.value?.checks.disk?.metrics?.usedPct ?? 0)

function checkMeta(status: string) {
  if (status === 'ok') return { label: '正常', type: 'success' as const }
  if (status === 'warn') return { label: '注意', type: 'warning' as const }
  return { label: '异常', type: 'danger' as const }
}

/** MB 数值转可读文本，超过 1GB 用 GB 展示 */
function fmtMB(mb: number) {
  if (!mb) return '0 MB'
  if (mb >= 1024) return `${(mb / 1024).toFixed(mb >= 10240 ? 0 : 1)} GB`
  return `${mb} MB`
}

function uptimeText(sec?: number) {
  if (!sec || sec < 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分`
}

async function load(silent = false) {
  if (silent) refreshing.value = true
  else loading.value = true
  try {
    data.value = await overviewApi.get()
  } catch (e: any) {
    if (!silent) ElMessage.error(e.message)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

onMounted(() => {
  void load()
  // 健康看板的价值在于「一直是对的」，30 秒自动刷新一次。
  timer = window.setInterval(() => void load(true), 30000)
})

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="pr-page" v-loading="loading">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">概览</h2>
        <p class="pr-subtitle">你好，{{ auth.displayName }}。这里是中控机的实时健康状态。</p>
      </div>
      <div class="head-actions">
        <span class="refresh-note">每 30 秒自动刷新</span>
        <el-button :loading="refreshing" @click="load(true)">刷新</el-button>
        <el-button v-if="auth.canDeploy" type="primary" @click="router.push('/deploy')">发起部署</el-button>
      </div>
    </div>

    <div class="stats">
      <div class="stat" :class="healthy ? 'is-ok' : 'is-bad'">
        <div class="label">中控机状态</div>
        <div class="value">{{ healthy ? '正常' : '异常' }}</div>
        <div class="foot">版本 {{ health?.version || '—' }} · 已运行 {{ uptimeText(health?.uptimeSec) }}</div>
      </div>
      <div class="stat">
        <div class="label">包仓库占用</div>
        <div class="value">{{ fmtMB(pkgUsed) }}</div>
        <el-progress
          class="stat-bar"
          :percentage="pkgPct"
          :stroke-width="6"
          :status="pkgPct >= 90 ? 'warning' : undefined"
        />
        <div class="foot">上限 {{ fmtMB(pkgMax) }} · {{ data?.packageTotal ?? 0 }} 个安装包</div>
      </div>
      <div class="stat">
        <div class="label">磁盘剩余</div>
        <div class="value">{{ fmtMB(diskFree) }}</div>
        <el-progress class="stat-bar" :percentage="diskUsedPct" :stroke-width="6" />
        <div class="foot">共 {{ fmtMB(diskTotal) }} · 已用 {{ diskUsedPct }}%</div>
      </div>
      <div class="stat">
        <div class="label">纳管服务器</div>
        <div class="value">{{ data?.serverTotal ?? 0 }}</div>
        <div class="foot">
          在线 {{ data?.serverOnline ?? 0 }} 台 · {{ data?.deployMode === 'ssh' ? 'SSH 模式' : '本地沙箱' }}
        </div>
      </div>
    </div>

    <div class="pr-card">
      <div class="card-head">
        <h3>健康检查</h3>
        <span class="metric-note">
          接口 <code>/healthz</code> · 检查时间
          {{ health ? new Date(health.time).toLocaleString('zh-CN', { hour12: false }) : '—' }}
        </span>
      </div>

      <div class="checks">
        <div v-for="c in checkRows" :key="c.key" class="check-row">
          <el-tag :type="checkMeta(c.status).type" size="small" effect="light" class="check-tag">
            {{ checkMeta(c.status).label }}
          </el-tag>
          <div class="check-main">
            <div class="check-name">{{ c.name }}</div>
            <div class="check-hint">{{ c.hint }}</div>
          </div>
          <div class="check-detail">{{ c.detail || '—' }}</div>
          <div class="check-latency">{{ c.latencyMs != null ? `${c.latencyMs} ms` : '' }}</div>
        </div>
      </div>

      <el-alert
        v-if="health && !healthy"
        class="health-alert"
        type="error"
        :closable="false"
        show-icon
        title="有硬性检查未通过，服务可能无法完成部署；请按上方明细排查（部署手册 §4.4）"
      />
      <div v-else class="check-note">
        硬性检查失败时 <code>/healthz</code> 返回 503，可直接用于负载均衡健康判定。
      </div>
    </div>

    <div class="pr-card" v-if="data?.serverMetrics?.length">
      <div class="card-head">
        <h3>服务器资源</h3>
        <span class="metric-note">按后台采集间隔自动更新，最新状态以「服务器」页检测为准</span>
      </div>
      <el-table :data="data.serverMetrics" style="width: 100%">
        <el-table-column label="服务器" min-width="160">
          <template #default="{ row }">
            <div class="srv-cell">
              <span class="srv-name">{{ row.serverName }}</span>
              <el-tag :type="envMeta(row.env).type" size="small" effect="light">{{ envMeta(row.env).label }}</el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="serverStatusMeta(row.status).type" size="small">{{ serverStatusMeta(row.status).label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="负载" width="90">
          <template #default="{ row }">
            <span v-if="row.metric?.status === 'online'">{{ row.metric.load1.toFixed(2) }}</span>
            <span v-else class="na">—</span>
          </template>
        </el-table-column>
        <el-table-column label="内存" min-width="150">
          <template #default="{ row }">
            <template v-if="row.metric?.status === 'online'">
              <div class="bar-line">
                <el-progress :percentage="memPct(row.metric) ?? 0" :stroke-width="8" />
              </div>
              <span class="bar-sub">{{ row.metric.memUsedMB }} / {{ row.metric.memTotalMB }} MB</span>
            </template>
            <span v-else class="na">—</span>
          </template>
        </el-table-column>
        <el-table-column label="磁盘（根分区）" min-width="170">
          <template #default="{ row }">
            <template v-if="row.metric?.status === 'online' && parseDirUsage(row.metric.dirUsage).length">
              <div class="bar-line">
                <el-progress :percentage="rootDiskPct(row.metric) ?? 0" :stroke-width="8" />
              </div>
              <span class="bar-sub pr-mono">{{ parseDirUsage(row.metric.dirUsage)[0].usedMB }} / {{ parseDirUsage(row.metric.dirUsage)[0].totalMB }} MB</span>
            </template>
            <span v-else class="na">—</span>
          </template>
        </el-table-column>
        <el-table-column label="采集时间" width="170">
          <template #default="{ row }">
            <span v-if="row.metric" class="probe-time">{{ new Date(row.metric.probedAt).toLocaleString('zh-CN', { hour12: false }) }}</span>
            <span v-else class="na">暂无数据</span>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<style scoped>
.head-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.refresh-note {
  font-size: 12px;
  color: #9aa1ae;
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-bottom: 18px;
}

.stat {
  background: #fff;
  border: 1px solid var(--pr-line);
  border-radius: 12px;
  padding: 18px 20px;
}

.stat.is-ok {
  border-left: 3px solid #37b26c;
}

.stat.is-bad {
  border-left: 3px solid #e34d59;
}

.label {
  font-size: 12px;
  color: var(--pr-sub);
}

.value {
  font-size: 28px;
  font-weight: 650;
  margin: 8px 0 6px;
  letter-spacing: 0.5px;
}

.stat-bar {
  margin: 2px 0 8px;
}

.foot {
  font-size: 12px;
  color: #9aa1ae;
}

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.card-head h3 {
  margin: 0;
  font-size: 15px;
}

.metric-note {
  font-size: 12px;
  color: #9aa1ae;
}

.checks {
  border: 1px solid var(--pr-line);
  border-radius: 10px;
  overflow: hidden;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--pr-line);
}

.check-row:last-child {
  border-bottom: none;
}

.check-tag {
  flex: 0 0 auto;
}

.check-main {
  flex: 0 0 200px;
}

.check-name {
  font-weight: 600;
  font-size: 13px;
}

.check-hint {
  font-size: 12px;
  color: #9aa1ae;
  margin-top: 2px;
}

.check-detail {
  flex: 1 1 auto;
  font-size: 13px;
  color: var(--pr-sub);
  word-break: break-all;
}

.check-latency {
  flex: 0 0 64px;
  text-align: right;
  font-size: 12px;
  color: #9aa1ae;
}

.health-alert {
  margin-top: 14px;
}

.check-note {
  margin-top: 12px;
  font-size: 12px;
  color: #9aa1ae;
}

code {
  font-family: var(--pr-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: 12px;
  background: #f3f4f7;
  padding: 1px 5px;
  border-radius: 4px;
}

.srv-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.srv-name {
  font-weight: 600;
}

.bar-line {
  max-width: 220px;
}

.bar-sub {
  display: block;
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 2px;
}

.probe-time {
  font-size: 12px;
  color: var(--pr-sub);
}

.na {
  color: #9aa1ae;
}

@media (max-width: 1000px) {
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }

  .check-row {
    flex-wrap: wrap;
  }

  .check-main {
    flex: 1 1 auto;
  }

  .check-detail {
    flex: 1 1 100%;
  }
}
</style>
