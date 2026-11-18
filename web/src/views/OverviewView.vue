<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { overviewApi, type Overview, type Task } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { envMeta, memPct, parseDirUsage, rootDiskPct, serverStatusMeta, statusMeta } from '@/utils/status'

const router = useRouter()
const auth = useAuthStore()
const data = ref<Overview | null>(null)
const loading = ref(true)

const failRate = computed(() => {
  const today = data.value?.taskToday ?? 0
  const failed = data.value?.taskFailed ?? 0
  return today > 0 ? `${Math.round((failed / today) * 100)}%` : '0%'
})

async function load() {
  loading.value = true
  try {
    data.value = await overviewApi.get()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function openTask(t: Task) {
  router.push(`/tasks/${t.id}`)
}

onMounted(load)
</script>

<template>
  <div class="pr-page" v-loading="loading">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">概览</h2>
        <p class="pr-subtitle">你好，{{ auth.displayName }}。这里是中控机的实时状态。</p>
      </div>
      <el-button v-if="auth.canDeploy" type="primary" @click="router.push('/deploy')">发起部署</el-button>
    </div>

    <div class="stats">
      <div class="stat">
        <div class="label">纳管服务器</div>
        <div class="value">{{ data?.serverTotal ?? 0 }}</div>
        <div class="foot">在线 {{ data?.serverOnline ?? 0 }} 台</div>
      </div>
      <div class="stat">
        <div class="label">今日任务</div>
        <div class="value">{{ data?.taskToday ?? 0 }}</div>
        <div class="foot">失败 {{ data?.taskFailed ?? 0 }} 次 · 失败率 {{ failRate }}</div>
      </div>
      <div class="stat">
        <div class="label">执行中</div>
        <div class="value">{{ data?.taskRunning ?? 0 }}</div>
        <div class="foot">并发闸门生效中</div>
      </div>
      <div class="stat">
        <div class="label">安装包</div>
        <div class="value">{{ data?.packageTotal ?? 0 }}</div>
        <div class="foot">执行模式 {{ data?.deployMode === 'ssh' ? 'SSH' : '本地沙箱' }}</div>
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

    <div class="pr-card">
      <div class="card-head">
        <h3>最近任务</h3>
        <el-button link type="primary" @click="router.push('/tasks')">全部任务</el-button>
      </div>

      <el-empty v-if="!data?.recentTasks?.length" description="还没有部署记录" :image-size="90" />

      <el-table v-else :data="data.recentTasks" style="width: 100%" @row-click="openTask">
        <el-table-column prop="taskNo" label="任务号" width="210">
          <template #default="{ row }">
            <span class="pr-mono">{{ row.taskNo }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="serverName" label="服务器" width="140" />
        <el-table-column prop="packageName" label="安装包" min-width="180" show-overflow-tooltip />
        <el-table-column prop="operator" label="操作人" width="110" />
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag :type="statusMeta(row.status).type" size="small" effect="light">
              {{ statusMeta(row.status).label }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="时间" width="160">
          <template #default="{ row }">
            <span class="time">{{ new Date(row.createdAt).toLocaleString('zh-CN', { hour12: false }) }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<style scoped>
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

.time {
  font-size: 12px;
  color: var(--pr-sub);
}

:deep(.el-table__row) {
  cursor: pointer;
}

@media (max-width: 1000px) {
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
