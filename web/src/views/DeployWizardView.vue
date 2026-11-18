<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { UploadFilled } from '@element-plus/icons-vue'
import {
  directoryApi,
  MAX_PACKAGE_BYTES,
  packageApi,
  scriptApi,
  serverApi,
  taskApi,
  type Directory,
  type Package,
  type Script,
  type Server,
} from '@/api'
import { envMeta, formatDateTime, formatSize, memPct, rootDiskPct, serverStatusMeta } from '@/utils/status'

const router = useRouter()

const step = ref(0)
const servers = ref<Server[]>([])
const dirs = ref<Directory[]>([])
const scripts = ref<Script[]>([])
const packages = ref<Package[]>([])

const form = reactive({
  serverId: 0,
  directoryId: 0,
  packageId: 0,
  preScriptId: undefined as number | undefined,
  scriptId: undefined as number | undefined,
  otp: '',
})

const uploading = ref(false)
const uploadPct = ref(0)
const uploadRetry = ref(0)
const uploadError = ref('')
const uploadedPkgId = ref(0)
const submitting = ref(false)
const loadingDirs = ref(false)

const selectedServer = computed(() => servers.value.find((s) => s.id === form.serverId))
const selectedDir = computed(() => dirs.value.find((d) => d.id === form.directoryId))
const selectedPkg = computed(() => packages.value.find((p) => p.id === form.packageId))
const preScripts = computed(() => scripts.value.filter((s) => s.phase === 'precheck'))
const postScripts = computed(() => scripts.value.filter((s) => s.phase === 'postdeploy'))
const selectedPreScript = computed(() => scripts.value.find((s) => s.id === form.preScriptId))
const selectedScript = computed(() => scripts.value.find((s) => s.id === form.scriptId))
const needOTP = computed(() => selectedServer.value?.env === 'prod')
const pkgReadyTitle = computed(() =>
  selectedPkg.value && selectedPkg.value.id === uploadedPkgId.value ? '上传成功，安装包已就绪' : '已选择安装包',
)
const pkgReadyType = computed(() =>
  selectedPkg.value && selectedPkg.value.id === uploadedPkgId.value ? 'success' : 'info',
)

const canNext = computed(() => {
  if (step.value === 0) return form.serverId > 0 && form.directoryId > 0
  if (step.value === 1) return form.packageId > 0
  return true
})

async function loadBase() {
  try {
    const [s, sc, pk] = await Promise.all([serverApi.list(), scriptApi.list(), packageApi.list()])
    servers.value = s
    scripts.value = sc
    packages.value = pk
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function pickServer(s: Server) {
  if (form.serverId === s.id) return
  if (s.status === 'offline') {
    ElMessage.warning(`服务器 ${s.name} 最近一次检测不可达，部署大概率失败，请先排查`)
  }
  form.serverId = s.id
  form.directoryId = 0
  dirs.value = []
  loadingDirs.value = true
  try {
    dirs.value = await directoryApi.list(s.id)
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loadingDirs.value = false
  }
}

async function doUpload(options: any) {
  const file: File = options.file
  const name = file.name.toLowerCase()
  uploadError.value = ''
  if (!name.endsWith('.zip') && !name.endsWith('.jar')) {
    uploadError.value = '仅支持 .zip 与 .jar 文件'
    ElMessage.error(uploadError.value)
    return
  }
  if (file.size > MAX_PACKAGE_BYTES) {
    uploadError.value = '单包不得超过 200MB'
    ElMessage.error(uploadError.value)
    return
  }
  uploading.value = true
  uploadPct.value = 0
  uploadRetry.value = 0
  try {
    const pkg = await packageApi.upload(file, (pct) => (uploadPct.value = pct), {
      onRetry: (attempt) => (uploadRetry.value = attempt),
    })
    packages.value.unshift(pkg)
    form.packageId = pkg.id
    uploadedPkgId.value = pkg.id
    uploadRetry.value = 0
    ElMessage.success('上传成功，文件校验通过')
    options.onSuccess?.(pkg)
  } catch (e: any) {
    uploadError.value = e.message
    ElMessage.error(e.message)
    options.onError?.(e)
  } finally {
    uploading.value = false
    uploadRetry.value = 0
  }
}

function selectPackage(p: Package) {
  form.packageId = p.id
  uploadError.value = ''
}

function next() {
  if (!canNext.value) {
    ElMessage.warning(step.value === 0 ? '请选择服务器与部署目录' : '请先上传安装包')
    return
  }
  step.value = Math.min(2, step.value + 1)
}

function prev() {
  step.value = Math.max(0, step.value - 1)
}

async function submit() {
  if (!form.serverId || !form.directoryId || !form.packageId) {
    ElMessage.warning('部署信息不完整')
    return
  }
  if (needOTP.value && form.otp.length !== 6) {
    ElMessage.warning('生产环境需要输入 6 位动态口令')
    return
  }
  submitting.value = true
  try {
    const task = await taskApi.create({
      serverId: form.serverId,
      directoryId: form.directoryId,
      packageId: form.packageId,
      preScriptId: form.preScriptId || undefined,
      scriptId: form.scriptId || undefined,
      otp: form.otp || undefined,
    })
    ElMessage.success('任务已受理')
    router.push(`/tasks/${task.id}`)
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    submitting.value = false
  }
}

onMounted(loadBase)
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">部署向导</h2>
        <p class="pr-subtitle">按流程选择目标、上传制品、绑定脚本，执行结果可在任务详情实时查看。</p>
      </div>
    </div>

    <el-steps :active="step" align-center finish-status="success" class="steps">
      <el-step title="选择目标" description="服务器与部署目录" />
      <el-step title="上传安装包" description="zip / jar" />
      <el-step title="脚本与确认" description="执行前复核" />
    </el-steps>

    <div class="wizard">
      <div class="panel pr-card">
        <!-- Step 1 -->
        <div v-show="step === 0">
          <h3 class="block-title">目标服务器</h3>
          <div class="server-grid">
            <div
              v-for="s in servers"
              :key="s.id"
              class="server-card"
              :class="{ active: form.serverId === s.id, prod: s.env === 'prod' }"
              @click="pickServer(s)"
            >
              <div class="sc-top">
                <span class="sc-name">{{ s.name }}</span>
                <el-tag :type="envMeta(s.env).type" size="small" effect="light">{{ envMeta(s.env).label }}</el-tag>
              </div>
              <div class="sc-addr pr-mono">{{ s.host }}:{{ s.port }}</div>
              <div class="sc-meta">
                <el-tag :type="serverStatusMeta(s.status).type" size="small" effect="plain">
                  {{ serverStatusMeta(s.status).label }}
                </el-tag>
                <span v-if="s.metrics?.status === 'online'">
                  内存 {{ memPct(s.metrics) ?? '—' }}% · 磁盘 {{ rootDiskPct(s.metrics) ?? '—' }}%
                </span>
                <span v-if="s.hostKeyFingerprint" class="sc-hk">指纹已登记</span>
              </div>
            </div>
            <el-empty v-if="!servers.length" description="还没有配置服务器" :image-size="80" />
          </div>

          <h3 class="block-title" v-if="form.serverId">选择部署目录</h3>
          <div v-loading="loadingDirs" class="dir-list">
            <el-radio-group v-if="dirs.length" v-model="form.directoryId" class="dir-group">
              <el-radio v-for="d in dirs" :key="d.id" :value="d.id" class="dir-item" border>
                <div class="dir-main">
                  <span class="pr-mono">{{ d.path }}</span>
                  <span class="dir-tags">
                    {{ d.allowOverwrite ? '可覆盖' : '仅新增' }} · {{ d.backupBeforeDeploy ? '部署前备份' : '不备份' }} · 保留 {{ d.keepReleases }} 版 · {{ d.packageMode === 'upload' ? '仅上传压缩包' : '自动解压' }}
                  </span>
                </div>
              </el-radio>
            </el-radio-group>
            <el-alert
              v-else-if="form.serverId"
              type="info"
              :closable="false"
              title="该服务器还没有配置部署目录"
              description="请先到「服务器 → 目录」中添加绝对路径与覆盖/备份策略。"
            />
          </div>
        </div>

        <!-- Step 2 -->
        <div v-show="step === 1">
          <h3 class="block-title">上传安装包</h3>
          <el-upload
            drag
            :show-file-list="false"
            :http-request="doUpload"
            accept=".zip,.jar"
            class="uploader"
          >
            <el-icon class="upload-icon"><UploadFilled /></el-icon>
            <div class="upload-text">拖拽 zip / jar 到此处，或点击选择文件</div>
            <div class="upload-hint">单包上限 200MB；上传后自动计算 SHA256 并在目标机二次校验</div>
          </el-upload>

          <div v-if="uploading" class="upload-progress">
            <el-progress :percentage="uploadPct" :stroke-width="8" :show-text="false" />
            <div class="upload-progress-text">
              {{ uploadRetry > 0 ? `网络中断，正在第 ${uploadRetry} 次自动重试…` : `正在上传并校验文件格式… ${uploadPct}%` }}
            </div>
          </div>

          <el-alert
            v-else-if="uploadError"
            type="error"
            :closable="false"
            show-icon
            class="upload-alert"
            :title="`上传失败：${uploadError}`"
          />

          <el-alert v-else-if="selectedPkg" :type="pkgReadyType" :closable="false" show-icon class="upload-alert">
            <template #title>{{ pkgReadyTitle }}</template>
            <div class="upload-ok-line">{{ selectedPkg.name }} · {{ formatSize(selectedPkg.size) }}</div>
            <div class="upload-ok-hash pr-mono">SHA256 {{ selectedPkg.sha256 }}</div>
            <div class="upload-ok-tip">
              {{
                selectedPkg.id === uploadedPkgId
                  ? '上传时已完成文件格式校验并登记 SHA256；部署时目标机还会再次校验。'
                  : '已选择仓库中的历史安装包；部署时目标机会校验其 SHA256。'
              }}
            </div>
          </el-alert>

          <div class="pkg-list" v-if="packages.length">
            <div class="pkg-head">最近上传</div>
            <div
              v-for="p in packages.slice(0, 8)"
              :key="p.id"
              class="pkg-item"
              :class="{ active: form.packageId === p.id }"
              @click="selectPackage(p)"
            >
              <el-tag size="small" effect="plain">{{ p.kind.toUpperCase() }}</el-tag>
              <span class="pkg-name">{{ p.name }}</span>
              <span class="pkg-time">{{ formatDateTime(p.createdAt) }}</span>
              <span class="pkg-size">{{ formatSize(p.size) }}</span>
              <span class="pkg-hash pr-mono">{{ p.sha256.slice(0, 12) }}…</span>
              <el-tag v-if="form.packageId === p.id" size="small" type="success" effect="light">已选择</el-tag>
            </div>
          </div>
        </div>

        <!-- Step 3 -->
        <div v-show="step === 2">
          <h3 class="block-title">前置校验脚本（可选）</h3>
          <el-select v-model="form.preScriptId" placeholder="不执行前置校验" clearable style="width: 100%">
            <el-option v-for="s in preScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
          </el-select>
          <div v-if="selectedPreScript" class="script-preview">
            <div class="sp-head">{{ selectedPreScript.description || '无描述' }}</div>
            <pre class="pr-log">{{ selectedPreScript.content }}</pre>
          </div>

          <h3 class="block-title">部署后脚本（可选）</h3>
          <el-select v-model="form.scriptId" placeholder="不绑定脚本（仅替换文件）" clearable style="width: 100%">
            <el-option v-for="s in postScripts" :key="s.id" :label="`${s.name}（${s.timeoutSec}s）`" :value="s.id" />
          </el-select>
          <div v-if="selectedScript" class="script-preview">
            <div class="sp-head">{{ selectedScript.description || '无描述' }}</div>
            <pre class="pr-log">{{ selectedScript.content }}</pre>
          </div>

          <template v-if="needOTP">
            <el-alert
              type="warning"
              :closable="false"
              show-icon
              title="生产环境操作需要二次认证"
              description="请输入认证器 App 中的 6 位动态口令，校验通过后才会受理任务。"
              class="otp-alert"
            />
            <el-input
              v-model="form.otp"
              maxlength="6"
              placeholder="6 位动态口令"
              class="otp"
            />
          </template>
        </div>

        <div class="wizard-foot">
          <el-button v-if="step > 0" @click="prev">上一步</el-button>
          <el-button v-if="step < 2" type="primary" @click="next">下一步</el-button>
          <el-button v-else type="primary" :loading="submitting" @click="submit">确认并执行</el-button>
        </div>
      </div>

      <aside class="summary pr-card">
        <h3 class="block-title">本次部署</h3>
        <dl>
          <dt>服务器</dt>
          <dd>
            <template v-if="selectedServer">
              {{ selectedServer.name }}
              <el-tag :type="envMeta(selectedServer.env).type" size="small" effect="light">{{ envMeta(selectedServer.env).label }}</el-tag>
            </template>
            <span v-else class="muted">未选择</span>
          </dd>

          <dt>目标目录</dt>
          <dd class="pr-mono">{{ selectedDir?.path || '—' }}</dd>

          <dt>安装包</dt>
          <dd>{{ selectedPkg ? `${selectedPkg.name}（${formatSize(selectedPkg.size)}）` : '—' }}</dd>

          <dt>SHA256</dt>
          <dd class="pr-mono hash">{{ selectedPkg ? selectedPkg.sha256.slice(0, 24) + '…' : '—' }}</dd>

          <dt>前置校验</dt>
          <dd>{{ selectedPreScript?.name || '不执行' }}</dd>

          <dt>部署后脚本</dt>
          <dd>{{ selectedScript?.name || '不执行' }}</dd>

          <dt>备份策略</dt>
          <dd>{{ selectedDir ? (selectedDir.backupBeforeDeploy ? `部署前备份，保留 ${selectedDir.keepReleases} 版` : '不备份') : '—' }}</dd>
        </dl>

        <div class="flow">
          <div class="flow-title">执行链路</div>
          <div class="flow-body pr-mono">NG → frp → 中控 → SSH → 目标机</div>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.steps {
  margin: 8px 0 18px;
  max-width: 820px;
}

.wizard {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 320px;
  gap: 16px;
  align-items: start;
}

.panel {
  min-height: 420px;
  display: flex;
  flex-direction: column;
}

.block-title {
  font-size: 14px;
  margin: 0 0 12px;
  font-weight: 600;
}

.block-title:not(:first-child) {
  margin-top: 24px;
}

.server-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}

.server-card {
  border: 1px solid var(--pr-line);
  border-radius: 10px;
  padding: 14px;
  cursor: pointer;
  transition: all 0.15s;
  background: #fff;
}

.server-card:hover {
  border-color: #b9c9f5;
}

.server-card.active {
  border-color: var(--pr-brand);
  background: var(--pr-brand-soft);
  box-shadow: 0 0 0 3px rgba(47, 107, 255, 0.08);
}

.server-card.prod.active {
  border-color: var(--pr-danger);
  background: #fdf2f2;
  box-shadow: 0 0 0 3px rgba(214, 69, 69, 0.08);
}

.sc-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.sc-name {
  font-weight: 600;
  font-size: 14px;
}

.sc-addr {
  color: var(--pr-sub);
  margin: 8px 0 6px;
}

.sc-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 6px;
  font-size: 12px;
  color: #9aa1ae;
}

.sc-hk {
  color: var(--el-color-success);
}

.dir-group {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
}

.dir-item {
  width: 100%;
  height: auto;
  padding: 12px 14px;
  margin: 0;
}

.dir-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  line-height: 1.5;
}

.dir-tags {
  font-size: 12px;
  color: var(--pr-sub);
  font-family: inherit;
}

.uploader :deep(.el-upload-dragger) {
  padding: 32px 20px;
  border-radius: 12px;
}

.upload-icon {
  font-size: 38px;
  color: #a9b6d6;
}

.upload-text {
  margin-top: 10px;
  font-size: 14px;
  color: #3d4658;
}

.upload-hint {
  margin-top: 6px;
  font-size: 12px;
  color: #9aa1ae;
}

.progress {
  margin-top: 16px;
}

.upload-progress {
  margin-top: 16px;
}

.upload-progress-text {
  margin-top: 6px;
  font-size: 12px;
  color: var(--pr-sub);
}

.upload-alert {
  margin-top: 16px;
  border-radius: 10px;
}

.upload-ok-line {
  font-size: 13px;
  color: #3d4658;
  margin-top: 4px;
}

.upload-ok-hash {
  font-size: 12px;
  color: #6b7280;
  margin-top: 4px;
  word-break: break-all;
}

.upload-ok-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 6px;
}

.pkg-list {
  margin-top: 20px;
}

.pkg-head {
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 8px;
}

.pkg-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
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

.script-preview {
  margin-top: 14px;
}

.sp-head {
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 8px;
}

.otp-alert {
  margin-top: 22px;
  border-radius: 10px;
}

.otp {
  margin-top: 12px;
  max-width: 260px;
}

.otp :deep(input) {
  letter-spacing: 6px;
  font-family: ui-monospace, monospace;
}

.wizard-foot {
  margin-top: auto;
  padding-top: 24px;
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.summary dl {
  margin: 0;
  font-size: 13px;
}

.summary dt {
  color: var(--pr-sub);
  font-size: 12px;
  margin-top: 14px;
}

.summary dd {
  margin: 4px 0 0;
  line-height: 1.5;
  word-break: break-all;
}

.summary dd.hash {
  color: #6b7280;
}

.muted {
  color: #9aa1ae;
}

.flow {
  margin-top: 22px;
  padding-top: 16px;
  border-top: 1px dashed var(--pr-line);
}

.flow-title {
  font-size: 12px;
  color: var(--pr-sub);
  margin-bottom: 8px;
}

.flow-body {
  background: #f7f9fd;
  border-radius: 8px;
  padding: 10px;
  color: #4b5563;
}

@media (max-width: 1100px) {
  .wizard {
    grid-template-columns: 1fr;
  }
}
</style>
