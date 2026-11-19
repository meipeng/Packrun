<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowLeft, Refresh } from '@element-plus/icons-vue'
import { taskApi, type Task, type TaskScriptSnapshot, type TaskStep } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { isRunning, statusMeta } from '@/utils/status'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const id = Number(route.params.id)

const task = ref<Task | null>(null)
const steps = ref<TaskStep[]>([])
const scripts = ref<TaskScriptSnapshot[]>([])
const loading = ref(true)
let timer: number | undefined

const running = computed(() => (task.value ? isRunning(task.value.status) : false))

const duration = computed(() => {
  if (!task.value?.startedAt) return '—'
  const end = task.value.finishedAt ? new Date(task.value.finishedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - new Date(task.value.startedAt).getTime()) / 1000))
  return sec < 60 ? `${sec} 秒` : `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
})

async function load() {
  try {
    const data = await taskApi.get(id)
    task.value = data.task
    steps.value = data.steps || []
    scripts.value = data.scripts || []
    if (!isRunning(data.task.status)) {
      stopPolling()
    }
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function stopPolling() {
  if (timer) {
    window.clearInterval(timer)
    timer = undefined
  }
}

async function cancel() {
  try {
    await ElMessageBox.confirm(
      '确认取消该任务？排队中的任务会直接取消；执行中的任务会尝试恢复部署前版本，恢复失败时需人工核对目标目录。',
      '取消任务',
      { type: 'warning' },
    )
    await taskApi.cancel(id)
    ElMessage.success('已发送取消指令')
    load()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(e.message || '取消失败')
  }
}

// 移除已结束的任务记录，回到任务列表。
async function removeTask() {
  try {
    await ElMessageBox.confirm(
      `确定移除任务 ${task.value?.taskNo}？步骤日志将一并删除，且不影响已发布结果与审计记录。`,
      '移除任务',
      { type: 'warning', confirmButtonText: '移除', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' },
    )
    await taskApi.remove(id)
    ElMessage.success('任务已移除')
    router.push('/tasks')
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(e.message || '移除失败')
  }
}

function stepType(status: string) {
  return status === 'success' ? 'success' : status === 'failed' ? 'danger' : status === 'running' ? 'primary' : 'info'
}

onMounted(async () => {
  await load()
  if (running.value) {
    timer = window.setInterval(load, 2000)
  }
})

onBeforeUnmount(stopPolling)
</script>

<template>
  <div class="pr-page" v-loading="loading">
    <div class="pr-page-head">
      <div class="head-left">
        <el-button :icon="ArrowLeft" text @click="router.push('/tasks')">返回</el-button>
        <div>
          <h2 class="pr-title">
            {{ task?.taskNo || '任务详情' }}
            <el-tag v-if="task" :type="statusMeta(task.status).type" size="small" effect="light" class="status-tag">
              {{ statusMeta(task.status).label }}
            </el-tag>
          </h2>
          <p class="pr-subtitle">{{ task?.message || '' }}</p>
        </div>
      </div>
      <div class="actions">
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button v-if="task && !running && auth.canDeploy" type="danger" plain @click="removeTask">移除任务</el-button>
        <el-button v-if="task && running && auth.canDeploy" type="danger" plain @click="cancel">取消任务</el-button>
      </div>
    </div>

    <div class="grid">
      <div class="pr-card">
        <div class="card-title">任务信息</div>
        <el-descriptions :column="2" size="small" border>
          <el-descriptions-item label="目标服务器">{{ task?.serverName }}</el-descriptions-item>
          <el-descriptions-item label="操作人">{{ task?.operator }}</el-descriptions-item>
          <el-descriptions-item label="部署目录" :span="2">
            <span class="pr-mono">{{ task?.targetPath }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="安装包">{{ task?.packageName }}</el-descriptions-item>
          <el-descriptions-item label="前置脚本">
            {{ task?.preScriptName ? `${task.preScriptName}（v${task.preScriptVersion}）` : '未绑定' }}
          </el-descriptions-item>
          <el-descriptions-item label="后置脚本">
            {{ task?.scriptName ? `${task.scriptName}（v${task.scriptVersion}）` : '未绑定' }}
          </el-descriptions-item>
          <el-descriptions-item label="动态口令">{{ task?.otpVerified ? '已校验（生产环境二次认证）' : '不需要 / 未校验' }}</el-descriptions-item>
          <el-descriptions-item label="耗时">{{ duration }}</el-descriptions-item>
          <el-descriptions-item label="创建时间" :span="2">
            {{ task ? new Date(task.createdAt).toLocaleString('zh-CN', { hour12: false }) : '' }}
          </el-descriptions-item>
        </el-descriptions>

        <div class="progress-box" v-if="task">
          <div class="progress-label">
            <span>执行进度</span>
            <span class="pr-mono">{{ task.stage }} · {{ task.progress }}%</span>
          </div>
          <el-progress :percentage="task.progress" :status="task.status === 'failed' ? 'exception' : undefined" :stroke-width="10" />
        </div>
      </div>

      <div class="pr-card">
        <div class="card-title">执行步骤</div>
        <el-timeline v-if="steps.length" class="timeline">
          <el-timeline-item
            v-for="s in steps"
            :key="s.id"
            :type="stepType(s.status)"
            :hollow="s.status !== 'success'"
            size="normal"
          >
            <div class="step-head">
              <span class="step-name">{{ s.name }}</span>
              <span class="step-time">{{ s.finishedAt ? new Date(s.finishedAt).toLocaleTimeString('zh-CN', { hour12: false }) : '进行中' }}</span>
            </div>
            <pre v-if="s.output" class="pr-log">{{ s.output }}</pre>
          </el-timeline-item>
        </el-timeline>
        <el-empty v-else description="等待执行" :image-size="80" />
      </div>
    </div>

    <div class="pr-card snapshot-card" v-if="scripts.length">
      <div class="card-title">执行脚本快照<span class="card-sub">受理时固化，脚本后续编辑不影响本任务</span></div>
      <el-collapse>
        <el-collapse-item v-for="s in scripts" :key="s.id" :name="String(s.id)">
          <template #title>
            {{ s.phase === 'precheck' ? '前置校验' : '部署后执行' }}：{{ s.name }}（v{{ s.version }}，超时 {{ s.timeoutSec }}s{{ s.runAs ? `，身份 ${s.runAs}` : '' }}）
          </template>
          <pre class="pr-log">{{ s.content }}</pre>
        </el-collapse-item>
      </el-collapse>
    </div>
  </div>
</template>

<style scoped>
.head-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.status-tag {
  margin-left: 8px;
  vertical-align: middle;
}

.actions {
  display: flex;
  gap: 8px;
}

.grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.05fr);
  gap: 16px;
  align-items: start;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 14px;
}

.snapshot-card {
  margin-top: 16px;
}

.card-sub {
  font-size: 11px;
  font-weight: 400;
  color: var(--pr-sub);
  margin-left: 8px;
}

.progress-box {
  margin-top: 20px;
}

.progress-label {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 8px;
}

.timeline {
  padding-left: 4px;
}

.step-head {
  display: flex;
  justify-content: space-between;
  margin-bottom: 8px;
}

.step-name {
  font-size: 13px;
  font-weight: 600;
}

.step-time {
  font-size: 11px;
  color: var(--pr-sub);
}

@media (max-width: 1100px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
