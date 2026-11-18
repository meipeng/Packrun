<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { auditApi, type AuditLog } from '@/api'

const rows = ref<AuditLog[]>([])
const total = ref(0)
const loading = ref(false)

const query = reactive({ page: 1, pageSize: 20, action: '', actor: '', keyword: '' })

const actionLabels: Record<string, string> = {
  'auth.login': '登录',
  'auth.password': '修改口令',
  'auth.otp.activate': '绑定动态口令',
  'auth.otp.rebind': '发起动态口令换绑',
  'auth.otp.rebind.confirm': '确认动态口令换绑',
  'auth.otp.disable': '解绑动态口令',
  'auth.recovery': '恢复码登录',
  'auth.recovery.regenerate': '重新生成恢复码',
  'server.create': '新增服务器',
  'server.update': '修改服务器',
  'server.delete': '删除服务器',
  'server.ping': '连通性检测',
  'directory.create': '新增目录',
  'directory.update': '修改目录',
  'directory.delete': '删除目录',
  'script.create': '新增脚本',
  'script.update': '修改脚本',
  'script.delete': '删除脚本',
  'package.upload': '上传安装包',
  'task.create': '发起部署',
  'task.finish': '部署结束',
  'task.cancel': '取消任务',
  'task.recover': '重启中断收口',
  'user.create': '新增用户',
  'user.update': '调整权限',
}

const actionOptions = Object.entries(actionLabels).map(([value, label]) => ({ value, label }))

async function load() {
  loading.value = true
  try {
    const data = await auditApi.list({ ...query })
    rows.value = data.items
    total.value = data.total
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

onMounted(load)
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">审计日志</h2>
        <p class="pr-subtitle">记录谁在什么时间、从哪个 IP、对哪个对象做了什么，以及动态口令是否通过。</p>
      </div>
    </div>

    <div class="pr-card">
      <div class="filters">
        <el-select v-model="query.action" placeholder="全部动作" clearable filterable style="width: 180px" @change="search">
          <el-option v-for="a in actionOptions" :key="a.value" :label="a.label" :value="a.value" />
        </el-select>
        <el-input v-model="query.actor" placeholder="操作人" clearable style="width: 150px" @keyup.enter="search" />
        <el-input v-model="query.keyword" placeholder="对象 / 任务号" clearable style="width: 220px" @keyup.enter="search" />
        <el-button type="primary" @click="search">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" style="width: 100%">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">
            <span class="time">{{ new Date(row.createdAt).toLocaleString('zh-CN', { hour12: false }) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="actor" label="操作人" width="110" />
        <el-table-column label="动作" width="140">
          <template #default="{ row }">
            <span class="action">{{ actionLabels[row.action] || row.action }}</span>
            <div class="action-raw pr-mono">{{ row.action }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="target" label="对象" min-width="180" show-overflow-tooltip />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag :type="row.result === 'ok' ? 'success' : 'danger'" size="small" effect="light">
              {{ row.result === 'ok' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="动态口令" width="100">
          <template #default="{ row }">
            <span class="otp" :class="{ ok: row.otpVerified }">{{ row.otpVerified ? '已校验' : '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="ip" label="来源 IP" width="130">
          <template #default="{ row }">
            <span class="pr-mono">{{ row.ip }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" min-width="200" show-overflow-tooltip />
        <el-table-column prop="taskNo" label="任务号" width="200">
          <template #default="{ row }">
            <span class="pr-mono">{{ row.taskNo || '—' }}</span>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
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

.time {
  font-size: 12px;
  color: var(--pr-sub);
}

.action {
  font-size: 13px;
}

.action-raw {
  color: #9aa1ae;
  font-size: 11px;
  margin-top: 2px;
}

.otp {
  font-size: 12px;
  color: #9aa1ae;
}

.otp.ok {
  color: #1f7a45;
  font-weight: 600;
}

.pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}
</style>
