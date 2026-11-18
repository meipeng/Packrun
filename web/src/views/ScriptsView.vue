<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { scriptApi, type Script } from '@/api'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const list = ref<Script[]>([])
const loading = ref(false)

const dialog = reactive({
  visible: false,
  saving: false,
  form: { id: 0, name: '', phase: 'postdeploy', content: '', timeoutSec: 120, runAs: '', description: '' },
})

const phaseLabel = computed(() => (p: string) => (p === 'precheck' ? '前置校验' : '部署后执行'))

async function load() {
  loading.value = true
  try {
    list.value = await scriptApi.list()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  dialog.form = { id: 0, name: '', phase: 'postdeploy', content: '', timeoutSec: 120, runAs: '', description: '' }
  dialog.visible = true
}

function openEdit(row: Script) {
  dialog.form = { ...row }
  dialog.visible = true
}

async function save() {
  if (!dialog.form.name || !dialog.form.content) {
    ElMessage.warning('请填写脚本名称与内容')
    return
  }
  dialog.saving = true
  try {
    if (dialog.form.id) {
      await scriptApi.update(dialog.form.id, dialog.form)
    } else {
      await scriptApi.create(dialog.form)
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

async function remove(row: Script) {
  try {
    await ElMessageBox.confirm(`确认删除脚本「${row.name}」？`, '删除确认', { type: 'warning' })
    await scriptApi.remove(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(e.message || '删除失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">脚本</h2>
        <p class="pr-subtitle">脚本以模板形式集中管理，执行时由目标机在部署目录内运行，受超时与执行身份约束。</p>
      </div>
      <el-button v-if="auth.isAdmin" type="primary" :icon="Plus" @click="openCreate">新增脚本</el-button>
    </div>

    <div class="pr-card">
      <el-table v-loading="loading" :data="list" style="width: 100%">
        <el-table-column prop="name" label="名称" width="180">
          <template #default="{ row }">
            <span class="name">{{ row.name }}</span>
            <span class="ver">v{{ row.version }}</span>
          </template>
        </el-table-column>
        <el-table-column label="阶段" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="row.phase === 'precheck' ? 'warning' : 'success'" effect="light">
              {{ phaseLabel(row.phase) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="说明" min-width="200" show-overflow-tooltip />
        <el-table-column prop="runAs" label="执行身份" width="110">
          <template #default="{ row }">{{ row.runAs || '默认' }}</template>
        </el-table-column>
        <el-table-column prop="timeoutSec" label="超时" width="90">
          <template #default="{ row }">{{ row.timeoutSec }}s</template>
        </el-table-column>
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEdit(row)">查看</el-button>
            <el-button v-if="auth.isAdmin" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="dialog.visible" :title="dialog.form.id ? '编辑脚本' : '新增脚本'" width="680px">
      <el-form label-width="88px">
        <el-form-item label="名称" required>
          <el-input v-model="dialog.form.name" placeholder="重启应用" />
        </el-form-item>
        <el-form-item label="阶段">
          <el-radio-group v-model="dialog.form.phase">
            <el-radio-button value="precheck">前置校验</el-radio-button>
            <el-radio-button value="postdeploy">部署后执行</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="执行身份">
          <el-input v-model="dialog.form.runAs" placeholder="留空则使用 SSH 登录用户" />
        </el-form-item>
        <el-form-item label="超时">
          <el-input-number v-model="dialog.form.timeoutSec" :min="1" :max="3600" />
          <span class="unit">秒，超时后强制终止</span>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="dialog.form.description" placeholder="简要说明脚本用途" />
        </el-form-item>
        <el-form-item label="脚本内容" required>
          <el-input v-model="dialog.form.content" type="textarea" :rows="9" class="code" placeholder="systemctl restart order-service" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">关闭</el-button>
        <el-button v-if="auth.isAdmin" type="primary" :loading="dialog.saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.name {
  font-weight: 600;
}

.ver {
  font-size: 11px;
  color: var(--pr-sub);
  margin-left: 6px;
}

.unit {
  margin-left: 8px;
  font-size: 12px;
  color: var(--pr-sub);
}

.code :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.7;
}
</style>
