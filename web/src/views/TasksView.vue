<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { taskApi, type Task } from '@/api'
import { isRunning, statusMeta } from '@/utils/status'

const router = useRouter()
const rows = ref<Task[]>([])
const total = ref(0)
const loading = ref(false)

const query = reactive({
  page: 1,
  pageSize: 20,
  status: '',
  keyword: '',
})

async function load() {
  loading.value = true
  try {
    const data = await taskApi.list({ ...query })
    rows.value = data.items
    total.value = data.total
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function reset() {
  query.status = ''
  query.keyword = ''
  query.page = 1
  load()
}

function search() {
  query.page = 1
  load()
}

function duration(t: Task) {
  if (!t.startedAt || !t.finishedAt) return '—'
  const sec = Math.max(0, Math.round((new Date(t.finishedAt).getTime() - new Date(t.startedAt).getTime()) / 1000))
  return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m ${sec % 60}s`
}

onMounted(load)
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">任务</h2>
        <p class="pr-subtitle">每一次部署都是一条可回溯的任务记录，任务号贯穿日志与审计。</p>
      </div>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </div>

    <div class="pr-card">
      <div class="filters">
        <el-input v-model="query.keyword" placeholder="任务号 / 包名 / 操作人" clearable style="width: 260px" @keyup.enter="search" />
        <el-select v-model="query.status" placeholder="全部状态" clearable style="width: 150px" @change="search">
          <el-option label="排队中" value="pending" />
          <el-option label="上传中" value="uploading" />
          <el-option label="校验中" value="verifying" />
          <el-option label="执行中" value="executing" />
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
          <el-option label="已取消" value="canceled" />
        </el-select>
        <el-button type="primary" @click="search">查询</el-button>
        <el-button @click="reset">重置</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" style="width: 100%" @row-click="(r: Task) => router.push(`/tasks/${r.id}`)">
        <el-table-column prop="taskNo" label="任务号" width="210">
          <template #default="{ row }">
            <span class="pr-mono">{{ row.taskNo }}</span>
          </template>
        </el-table-column>
        <el-table-column label="服务器" width="150">
          <template #default="{ row }">
            <div class="srv">{{ row.serverName }}</div>
            <div class="path pr-mono">{{ row.targetPath }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="packageName" label="安装包" min-width="170" show-overflow-tooltip />
        <el-table-column label="脚本" width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <div>{{ row.scriptName ? `${row.scriptName}（v${row.scriptVersion}）` : '—' }}</div>
            <div v-if="row.preScriptName" class="pre">前置：{{ row.preScriptName }}（v{{ row.preScriptVersion }}）</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="180">
          <template #default="{ row }">
            <el-tag :type="statusMeta(row.status).type" size="small" effect="light">{{ statusMeta(row.status).label }}</el-tag>
            <el-progress
              v-if="isRunning(row.status)"
              :percentage="row.progress"
              :stroke-width="4"
              :show-text="false"
              class="mini-progress"
            />
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="操作人" width="100" />
        <el-table-column label="耗时" width="95">
          <template #default="{ row }">{{ duration(row) }}</template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">
            <span class="time">{{ new Date(row.createdAt).toLocaleString('zh-CN', { hour12: false }) }}</span>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          @current-change="load"
          @size-change="load"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  gap: 10px;
  margin-bottom: 14px;
  flex-wrap: wrap;
}

.srv {
  font-size: 13px;
  font-weight: 500;
}

.path {
  color: var(--pr-sub);
  margin-top: 2px;
  font-size: 11px;
}

.mini-progress {
  margin-top: 6px;
  width: 120px;
}

.pre {
  font-size: 11px;
  color: var(--pr-sub);
  margin-top: 2px;
}

.time {
  font-size: 12px;
  color: var(--pr-sub);
}

.pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}

:deep(.el-table__row) {
  cursor: pointer;
}
</style>
