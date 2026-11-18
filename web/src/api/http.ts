import axios from 'axios'
import { appBasePath } from '@/utils/base'

const basePath = appBasePath()

const http = axios.create({
  baseURL: `${basePath}api/v1`,
  timeout: 60000,
})

http.interceptors.request.use((config) => {
  const token = localStorage.getItem('packrun_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (res) => {
    const body = res.data
    if (body && typeof body.code === 'number' && body.code !== 0) {
      return Promise.reject(Object.assign(new Error(body.message), { code: body.code }))
    }
    return body.data
  },
  (error) => {
    const status = error.response?.status
    const body = error.response?.data
    const code = body?.code
    // 无响应的网络层错误直接透出 axios 原文（如 "timeout of 900000ms exceeded"）用户看不懂，
    // 这里统一换成可读提示；有大包上传场景，超时尤其常见。
    const offline = !error.response
    const timedOut = error.code === 'ECONNABORTED' || /timeout/i.test(String(error.message || ''))
    let message = body?.message || error.message || '网络异常'
    if (offline) {
      message = timedOut ? '请求超时，请检查网络后重试' : '网络连接中断，请检查网络后重试'
    }
    // 仅网络中断与网关类错误值得重试；参数、权限、配额类重试没有意义。
    const retryable = offline || status === 429 || status === 502 || status === 503 || status === 504

    // 1428 表示需要动态口令，属于可继续的交互，不跳登录
    if (status === 401 && code !== 1428 && code !== 1003) {
      localStorage.removeItem('packrun_token')
      if (location.pathname !== `${basePath}login`) {
        location.href = `${basePath}login`
      }
    }
    return Promise.reject(Object.assign(new Error(message), { code, status, retryable }))
  },
)

export default http
