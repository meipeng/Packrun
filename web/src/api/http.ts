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
    const message = body?.message || error.message || '网络异常'

    // 1428 表示需要动态口令，属于可继续的交互，不跳登录
    if (status === 401 && code !== 1428 && code !== 1003) {
      localStorage.removeItem('packrun_token')
      if (location.pathname !== `${basePath}login`) {
        location.href = `${basePath}login`
      }
    }
    return Promise.reject(Object.assign(new Error(message), { code, status }))
  },
)

export default http
