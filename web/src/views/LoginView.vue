<script setup lang="ts">
import { reactive, ref, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Lock, User, Key } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const form = reactive({ username: '', password: '', otp: '' })
const loading = ref(false)
const needOtp = ref(false)
const useRecovery = ref(false)
const otpRef = ref()

function toggleRecovery() {
  useRecovery.value = !useRecovery.value
  form.otp = ''
}

async function submit() {
  if (!form.username || !form.password) {
    ElMessage.warning('请输入账号与口令')
    return
  }
  if (needOtp.value) {
    const normalized = form.otp.replace(/[-\s]/g, '')
    if (useRecovery.value && normalized.length !== 16) {
      ElMessage.warning('请输入 16 位恢复码')
      return
    }
    if (!useRecovery.value && form.otp.length !== 6) {
      ElMessage.warning('请输入 6 位动态口令')
      return
    }
  }
  loading.value = true
  try {
    await auth.login({ ...form })
    ElMessage.success('登录成功')
    const redirect = (route.query.redirect as string) || '/'
    router.replace(redirect)
  } catch (e: any) {
    if (e.code === 1428 || e.code === 1003) {
      needOtp.value = true
      form.otp = ''
      await nextTick()
      otpRef.value?.focus()
      ElMessage.warning(e.message)
    } else {
      ElMessage.error(e.message || '登录失败')
    }
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login">
    <section class="brand-panel">
      <div class="brand-top">
        <div class="mark">P</div>
        <span>Packrun</span>
      </div>
      <h1>上传即部署<br />把发布动作收进一条可控链路</h1>
      <p class="lede">
        选服务器 → 选目录 → 上传 zip / jar → 绑定脚本 → 执行回执。<br />
        中控机统一纳管，业务服务器不对公网暴露。
      </p>
      <ul class="facts">
        <li><span>01</span>口令 + 动态口令双因子</li>
        <li><span>02</span>生产环境操作强制再认证</li>
        <li><span>03</span>全链路任务号与审计留痕</li>
      </ul>
      <div class="chain">
        <code>外网 → Nginx → frp → Go 中控 → SSH → 业务机</code>
      </div>
    </section>

    <section class="form-panel">
      <div class="form-box">
        <h2>登录控制台</h2>
        <p class="hint">使用平台账号登录，账号口令由管理员分配</p>

        <el-form @submit.prevent="submit">
          <el-form-item>
            <el-input v-model="form.username" size="large" placeholder="账号" :prefix-icon="User" autocomplete="username" />
          </el-form-item>
          <el-form-item>
            <el-input
              v-model="form.password"
              size="large"
              type="password"
              show-password
              placeholder="登录口令"
              :prefix-icon="Lock"
              autocomplete="current-password"
              @keyup.enter="submit"
            />
          </el-form-item>

          <el-form-item v-if="needOtp">
            <el-input
              ref="otpRef"
              v-model="form.otp"
              size="large"
              :maxlength="useRecovery ? 19 : 6"
              :placeholder="useRecovery ? 'XXXX-XXXX-XXXX-XXXX' : '6 位动态口令'"
              :prefix-icon="Key"
              class="otp-input"
              :class="{ recovery: useRecovery }"
              @keyup.enter="submit"
            />
            <div class="otp-tip">
              {{ useRecovery ? '输入个人设置中生成的恢复码，每枚仅可使用一次' : '打开认证器 App 读取当前口令，每 30 秒刷新' }}
            </div>
            <div class="otp-toggle">
              <el-button link type="primary" @click="toggleRecovery">
                {{ useRecovery ? '改用动态口令' : '认证器不可用？使用恢复码' }}
              </el-button>
            </div>
          </el-form-item>

          <el-button type="primary" size="large" class="submit" :loading="loading" @click="submit">
            {{ needOtp ? '验证并登录' : '登录' }}
          </el-button>
        </el-form>

        <div class="foot">
          <span>首次部署默认账号 admin</span>
          <span class="dot">·</span>
          <span>口令见 configs/config.yaml</span>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.login {
  min-height: 100%;
  display: grid;
  grid-template-columns: 1.05fr 0.95fr;
  background: #fff;
}

.brand-panel {
  background: radial-gradient(1200px 500px at 10% 10%, #2b4fd6 0%, #16205a 45%, #0d1330 100%);
  color: #fff;
  padding: 56px 60px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  position: relative;
  overflow: hidden;
}

.brand-panel::after {
  content: '';
  position: absolute;
  inset: auto -120px -180px auto;
  width: 460px;
  height: 460px;
  border-radius: 50%;
  background: radial-gradient(circle, rgba(110, 155, 255, 0.35), transparent 65%);
}

.brand-top {
  display: flex;
  align-items: center;
  gap: 10px;
  font-weight: 700;
  letter-spacing: 0.6px;
  margin-bottom: 42px;
  font-size: 15px;
}

.mark {
  width: 34px;
  height: 34px;
  border-radius: 9px;
  background: linear-gradient(135deg, #6d9bff, #2f6bff);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 16px;
}

h1 {
  font-size: 34px;
  line-height: 1.35;
  margin: 0 0 18px;
  font-weight: 650;
  letter-spacing: 0.5px;
}

.lede {
  color: rgba(255, 255, 255, 0.72);
  font-size: 14px;
  line-height: 1.9;
  margin: 0 0 34px;
}

.facts {
  list-style: none;
  padding: 0;
  margin: 0 0 40px;
  display: flex;
  flex-direction: column;
  gap: 13px;
  font-size: 13px;
  color: rgba(255, 255, 255, 0.85);
}

.facts span {
  display: inline-block;
  width: 26px;
  color: #7ea6ff;
  font-family: ui-monospace, monospace;
}

.chain code {
  font-size: 12px;
  color: rgba(255, 255, 255, 0.55);
  background: rgba(255, 255, 255, 0.07);
  border: 1px solid rgba(255, 255, 255, 0.12);
  padding: 8px 12px;
  border-radius: 8px;
}

.form-panel {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
}

.form-box {
  width: 100%;
  max-width: 380px;
}

h2 {
  margin: 0 0 8px;
  font-size: 24px;
  font-weight: 650;
}

.hint {
  margin: 0 0 28px;
  color: var(--pr-sub);
  font-size: 13px;
}

.submit {
  width: 100%;
  margin-top: 6px;
  height: 44px;
  font-size: 15px;
}

.otp-tip {
  font-size: 12px;
  color: var(--pr-sub);
  margin-top: 6px;
}

.otp-input :deep(input) {
  letter-spacing: 8px;
  font-family: ui-monospace, monospace;
  font-size: 18px;
}

.otp-input.recovery :deep(input) {
  letter-spacing: 1.5px;
  font-size: 15px;
}

.otp-toggle {
  margin-top: 2px;
  text-align: right;
  width: 100%;
}

.foot {
  margin-top: 26px;
  font-size: 12px;
  color: #9aa1ae;
  text-align: center;
}

.dot {
  margin: 0 6px;
}

@media (max-width: 900px) {
  .login {
    grid-template-columns: 1fr;
  }
  .brand-panel {
    display: none;
  }
}
</style>
