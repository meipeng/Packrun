<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Key, Lock } from '@element-plus/icons-vue'
import QrcodeVue from 'qrcode.vue'
import { authApi } from '@/api'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const setup = ref<{ secret: string; otpauth: string; issuer: string; period: number } | null>(null)
const otpCode = ref('')
const activating = ref(false)

const pwd = reactive({ oldPassword: '', newPassword: '', confirm: '', otp: '' })
const changing = ref(false)
const disableCode = ref('')
const disabling = ref(false)
const rebinding = ref(false)

const recoveryCodes = ref<string[]>([])
const recoveryDialog = ref(false)
const recovery = reactive({ total: 0, remaining: 0 })
const regenCode = ref('')
const regenerating = ref(false)

async function loadRecoveryStatus() {
  try {
    const data = await authApi.recoveryStatus()
    recovery.total = data.total
    recovery.remaining = data.remaining
  } catch {
    // 恢复码余量仅用于展示，失败不阻断页面
  }
}

async function startSetup() {
  try {
    setup.value = await authApi.otpSetup()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function activate() {
  if (otpCode.value.length !== 6) {
    ElMessage.warning('请输入 6 位动态口令')
    return
  }
  activating.value = true
  const isRebind = !!auth.user?.otpEnabled
  try {
    const res = await authApi.otpActivate(otpCode.value)
    ElMessage.success(isRebind ? '动态口令已换绑' : '动态口令已绑定')
    setup.value = null
    otpCode.value = ''
    if (res.recoveryCodes?.length) {
      recoveryCodes.value = res.recoveryCodes
      recoveryDialog.value = true
    }
    await auth.fetchMe()
    await loadRecoveryStatus()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    activating.value = false
  }
}

async function startRebind() {
  if (disableCode.value.length !== 6) {
    ElMessage.warning('请输入当前动态口令')
    return
  }
  rebinding.value = true
  try {
    setup.value = await authApi.otpRebind(disableCode.value)
    otpCode.value = ''
    disableCode.value = ''
    ElMessage.success('已生成换绑种子，请在认证器中添加新账号')
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    rebinding.value = false
  }
}

async function disable() {
  if (disableCode.value.length !== 6) {
    ElMessage.warning('请输入当前动态口令')
    return
  }
  try {
    await ElMessageBox.confirm(
      '解绑后登录不再需要动态口令，生产环境部署将无法发起，恢复码也会全部清空。确认解绑？',
      '解绑动态口令',
      { type: 'warning' },
    )
  } catch {
    return
  }
  disabling.value = true
  try {
    await authApi.otpDisable(disableCode.value)
    ElMessage.success('已解绑')
    disableCode.value = ''
    recovery.total = 0
    recovery.remaining = 0
    recoveryCodes.value = []
    await auth.fetchMe()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    disabling.value = false
  }
}

async function regenerateRecoveryCodes() {
  if (regenCode.value.length !== 6) {
    ElMessage.warning('请输入当前动态口令')
    return
  }
  regenerating.value = true
  try {
    const res = await authApi.regenerateRecoveryCodes(regenCode.value)
    recoveryCodes.value = res.recoveryCodes
    recoveryDialog.value = true
    regenCode.value = ''
    await loadRecoveryStatus()
    ElMessage.success('已生成新的恢复码，旧码全部失效')
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    regenerating.value = false
  }
}

async function copyRecoveryCodes() {
  if (!recoveryCodes.value.length) return
  try {
    await navigator.clipboard.writeText(recoveryCodes.value.join('\n'))
    ElMessage.success('已复制全部恢复码')
  } catch {
    ElMessage.warning('复制失败，请手动记录')
  }
}

async function changePassword() {
  if (!pwd.oldPassword || pwd.newPassword.length < 8) {
    ElMessage.warning('新口令至少 8 位')
    return
  }
  if (pwd.newPassword !== pwd.confirm) {
    ElMessage.warning('两次输入的新口令不一致')
    return
  }
  if (auth.user?.otpEnabled && pwd.otp.length !== 6) {
    ElMessage.warning('请输入 6 位动态口令')
    return
  }
  changing.value = true
  try {
    await authApi.changePassword({
      oldPassword: pwd.oldPassword,
      newPassword: pwd.newPassword,
      ...(auth.user?.otpEnabled ? { otp: pwd.otp } : {}),
    })
    ElMessage.success('口令已更新')
    pwd.oldPassword = ''
    pwd.newPassword = ''
    pwd.confirm = ''
    pwd.otp = ''
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    changing.value = false
  }
}

async function copySecret() {
  if (!setup.value) return
  try {
    await navigator.clipboard.writeText(setup.value.secret)
    ElMessage.success('已复制种子')
  } catch {
    ElMessage.warning('复制失败，请手动选择文本')
  }
}

onMounted(async () => {
  if (!auth.user) await auth.fetchMe()
  if (auth.user?.otpEnabled) await loadRecoveryStatus()
})
</script>

<template>
  <div class="pr-page">
    <div class="pr-page-head">
      <div>
        <h2 class="pr-title">个人设置</h2>
        <p class="pr-subtitle">动态口令是第二因子；生产环境部署时还会作为高危操作的再认证凭据。</p>
      </div>
    </div>

    <div class="grid">
      <div class="pr-card">
        <div class="card-title">账号信息</div>
        <el-descriptions :column="1" size="small" border>
          <el-descriptions-item label="账号">{{ auth.user?.username }}</el-descriptions-item>
          <el-descriptions-item label="显示名">{{ auth.user?.displayName || '—' }}</el-descriptions-item>
          <el-descriptions-item label="角色">
            {{ auth.user?.role === 'admin' ? '管理员' : auth.user?.role === 'auditor' ? '审计员' : '发布者' }}
          </el-descriptions-item>
          <el-descriptions-item label="数据范围">{{ auth.user?.serverScope === '*' ? '全部服务器' : auth.user?.serverScope }}</el-descriptions-item>
          <el-descriptions-item label="最近登录">
            {{ auth.user?.lastLoginAt ? new Date(auth.user.lastLoginAt).toLocaleString('zh-CN', { hour12: false }) : '—' }}
          </el-descriptions-item>
        </el-descriptions>
      </div>

      <div class="pr-card">
        <div class="card-title">
          动态口令
          <el-tag :type="auth.user?.otpEnabled ? 'success' : 'warning'" size="small" effect="light" class="tag">
            {{ auth.user?.otpEnabled ? '已绑定' : '未绑定' }}
          </el-tag>
        </div>

        <template v-if="!auth.user?.otpEnabled">
          <el-alert
            type="info"
            :closable="false"
            title="未绑定动态口令时，无法在生产环境发起部署"
            description="绑定后登录需同时输入口令与动态口令，每 30 秒刷新一次。"
          />

          <el-button v-if="!setup" type="primary" :icon="Key" class="mt" @click="startSetup">生成绑定种子</el-button>
        </template>

        <template v-else>
          <p class="desc">当前账号已启用双因子登录。换绑或解绑需要输入当前动态口令。</p>
          <div class="activate-row">
            <el-input v-model="disableCode" maxlength="6" placeholder="当前 6 位口令" class="otp" @keyup.enter="startRebind" />
            <el-button type="primary" plain :loading="rebinding" @click="startRebind">换绑</el-button>
            <el-button type="danger" plain :loading="disabling" @click="disable">解绑</el-button>
          </div>
        </template>

        <div v-if="setup" class="setup">
          <el-alert
            v-if="auth.user?.otpEnabled"
            type="warning"
            :closable="false"
            show-icon
            title="换绑待确认"
            description="原动态口令仍然有效；录入新种子并输入新口令后才会切换，同时重新生成恢复码。"
          />
          <div class="label">1. 用认证器 App 扫描二维码</div>
          <div class="qr-box">
            <QrcodeVue :value="setup.otpauth" :size="160" :margin="2" level="M" render-as="svg" />
          </div>
          <div class="meta qr-meta">
            签发方 {{ setup.issuer }} · 周期 {{ setup.period }}s · 算法 SHA1 · 6 位
          </div>

          <div class="label">无法扫码？手动录入种子</div>
          <div class="secret-box">
            <span class="pr-mono secret">{{ setup.secret }}</span>
            <el-button link :icon="CopyDocument" @click="copySecret">复制</el-button>
          </div>

          <div class="label">2. 输入 App 显示的 6 位口令完成{{ auth.user?.otpEnabled ? '换绑' : '绑定' }}</div>
          <div class="activate-row">
            <el-input v-model="otpCode" maxlength="6" placeholder="000000" class="otp" />
            <el-button type="primary" :loading="activating" @click="activate">
              {{ auth.user?.otpEnabled ? '完成换绑' : '完成绑定' }}
            </el-button>
          </div>
        </div>
      </div>

      <div v-if="auth.user?.otpEnabled" class="pr-card">
        <div class="card-title">
          登录恢复码
          <el-tag type="info" size="small" effect="light" class="tag">{{ recovery.remaining }} / {{ recovery.total }} 可用</el-tag>
        </div>
        <p class="desc">认证器丢失或不可用时，可用恢复码代替动态口令登录。每枚恢复码只能使用一次。</p>
        <el-alert
          type="warning"
          :closable="false"
          show-icon
          title="恢复码仅在生成时展示一次"
          description="请复制并离线保存。重新生成会立即作废全部旧码。"
        />
        <div class="activate-row mt">
          <el-input v-model="regenCode" maxlength="6" placeholder="当前 6 位动态口令" class="otp" @keyup.enter="regenerateRecoveryCodes" />
          <el-button type="primary" plain :loading="regenerating" @click="regenerateRecoveryCodes">重新生成</el-button>
        </div>
      </div>

      <div class="pr-card">
        <div class="card-title">修改登录口令</div>
        <el-form label-width="88px">
          <el-form-item label="原口令">
            <el-input v-model="pwd.oldPassword" type="password" show-password :prefix-icon="Lock" />
          </el-form-item>
          <el-form-item label="新口令">
            <el-input v-model="pwd.newPassword" type="password" show-password placeholder="至少 8 位" />
          </el-form-item>
          <el-form-item label="确认">
            <el-input v-model="pwd.confirm" type="password" show-password />
          </el-form-item>
          <el-form-item v-if="auth.user?.otpEnabled" label="动态口令">
            <el-input v-model="pwd.otp" maxlength="6" placeholder="当前 6 位口令" />
          </el-form-item>
          <el-form-item>
            <el-button type="primary" :loading="changing" @click="changePassword">更新口令</el-button>
          </el-form-item>
        </el-form>
      </div>
    </div>

    <el-dialog v-model="recoveryDialog" title="保存登录恢复码" width="560px">
      <el-alert
        type="warning"
        :closable="false"
        title="仅本次展示，关闭后无法再次查看明文"
        description="每枚恢复码只能登录一次；请复制后离线保存。"
      />
      <div class="rc-grid">
        <span v-for="code in recoveryCodes" :key="code" class="rc-code pr-mono">{{ code }}</span>
      </div>
      <template #footer>
        <el-button :icon="CopyDocument" @click="copyRecoveryCodes">复制全部</el-button>
        <el-button type="primary" @click="recoveryDialog = false">我已知晓</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 14px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.tag {
  font-weight: 400;
}

.mt {
  margin-top: 16px;
}

.setup {
  margin-top: 18px;
}

.label {
  font-size: 12px;
  color: var(--pr-sub);
  margin: 14px 0 8px;
}

.qr-box {
  display: flex;
  justify-content: center;
  padding: 12px;
  background: #fff;
  border: 1px solid var(--pr-line);
  border-radius: 10px;
}

.qr-meta {
  text-align: center;
}

.secret-box {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  background: #f7f9fd;
  border: 1px dashed var(--pr-line);
  border-radius: 8px;
  padding: 12px 14px;
}

.secret {
  font-size: 14px;
  letter-spacing: 1.5px;
  word-break: break-all;
}

.meta {
  font-size: 11px;
  color: #9aa1ae;
  margin-top: 8px;
}

.activate-row {
  display: flex;
  gap: 10px;
  align-items: center;
}

.otp {
  max-width: 200px;
}

.otp :deep(input) {
  letter-spacing: 6px;
  font-family: ui-monospace, monospace;
}

.desc {
  font-size: 13px;
  color: var(--pr-sub);
  line-height: 1.7;
  margin: 0 0 14px;
}

.rc-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  margin-top: 16px;
}

.rc-code {
  text-align: center;
  padding: 9px 10px;
  border: 1px dashed var(--pr-line);
  border-radius: 8px;
  background: #f7f9fd;
  font-size: 13px;
  letter-spacing: 1px;
  user-select: all;
}

@media (max-width: 1000px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
