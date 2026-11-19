import http from './http'

export interface User {
  id: number
  username: string
  displayName: string
  role: string
  otpEnabled: boolean
  serverScope: string
  parentId?: number | null
  status: string
  lastLoginAt?: string
  createdAt: string
}

export interface Server {
  id: number
  name: string
  env: 'dev' | 'test' | 'prod' | string
  host: string
  port: number
  username: string
  authType: 'key' | 'password' | string
  credentialRef: string
  status: string
  maxConcurrent: number
  remark: string
  hostKeyAlgo?: string
  hostKeyFingerprint?: string
  hostKeyPinnedAt?: string
  metrics?: ServerMetric | null
}

/** 部署目录所在文件系统的占用快照 */
export interface DirUsage {
  path: string
  totalMB: number
  usedMB: number
  pct: number
}

/** 服务器最近一次运行指标快照 */
export interface ServerMetric {
  id: number
  serverId: number
  status: string
  message: string
  hostname: string
  os: string
  uptimeSec: number
  cpuCores: number
  load1: number
  load5: number
  load15: number
  memTotalMB: number
  memUsedMB: number
  /** JSON 数组文本：[]DirUsage，用 parseDirUsage 解析 */
  dirUsage: string
  probeMode: string
  probedAt: string
}

/** 主机指纹采集结果 */
export interface HostKeyInfo {
  algo: string
  fingerprint: string
  pinned: boolean
  matched: boolean
}

/** 手动检测结果：指标快照 + 凭据自检 */
export interface ProbeResult {
  status: string
  message: string
  metric: ServerMetric
  credential: { ok: boolean; message: string }
  mode: string
}

export interface Directory {
  id: number
  serverId: number
  path: string
  owner: string
  allowOverwrite: boolean
  backupBeforeDeploy: boolean
  packageMode: 'unpack' | 'upload' | string
  keepReleases: number
  maxSizeMB: number
  remark: string
}

export interface Script {
  id: number
  name: string
  phase: 'precheck' | 'postdeploy' | string
  content: string
  timeoutSec: number
  runAs: string
  version: number
  description: string
}

export interface Package {
  id: number
  name: string
  kind: string
  size: number
  sha256: string
  createdAt: string
}

export interface Task {
  id: number
  taskNo: string
  serverId: number
  serverName: string
  directoryId: number
  targetPath: string
  packageId: number
  packageName: string
  preScriptId?: number
  preScriptName: string
  preScriptVersion: number
  scriptId?: number
  scriptName: string
  scriptVersion: number
  projectId: number
  batchNo: string
  kind: 'deploy' | 'rollback' | string
  projectVersion: string
  remark: string
  operator: string
  status: string
  stage: string
  progress: number
  message: string
  otpVerified: boolean
  startedAt?: string
  finishedAt?: string
  createdAt: string
}

export interface TaskStep {
  id: number
  seq: number
  name: string
  status: string
  output: string
  startedAt?: string
  finishedAt?: string
}

export interface AuditLog {
  id: number
  actor: string
  action: string
  target: string
  result: string
  ip: string
  taskNo: string
  otpVerified: boolean
  detail: string
  createdAt: string
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

/** 概览里的服务器资源摘要行 */
export interface ServerMetricRow {
  serverName: string
  env: string
  status: string
  metric: ServerMetric | null
}

/** 单项健康检查结果：ok / warn 不影响可用性，fail 会整体返回 503 */
export interface HealthCheck {
  status: 'ok' | 'warn' | 'fail'
  detail?: string
  latencyMs?: number
  /** 结构化指标：包仓库为 usedMB/maxMB/pct，磁盘为 freeMB/totalMB/usedPct */
  metrics?: Record<string, number>
}

export interface HealthReport {
  status: 'ok' | 'degraded'
  version: string
  uptimeSec: number
  time: string
  checks: Record<string, HealthCheck>
}

export interface Overview {
  serverTotal: number
  serverOnline: number
  packageTotal: number
  deployMode: string
  serverMetrics: ServerMetricRow[]
  health: HealthReport
}

// ---------- 工作项目 ----------

export interface ProjectDir {
  id: number
  serverId: number
  serverName: string
  serverEnv: string
  path: string
  owner: string
  allowOverwrite: boolean
  backupBeforeDeploy: boolean
  packageMode: 'unpack' | 'upload' | string
  keepReleases: number
  maxSizeMB: number
  remark: string
}

export interface ProjectMemberInfo {
  id: number
  username: string
  displayName: string
  role: string
}

export interface Project {
  id: number
  name: string
  code: string
  description: string
  ownerId: number
  scriptId?: number
  scriptName?: string
  version: string
  status: string
  createdAt: string
  updatedAt: string
  dirs: ProjectDir[]
  members: ProjectMemberInfo[]
}

export interface ProjectTaskBrief {
  id: number
  taskNo: string
  serverName: string
  targetPath: string
  status: string
  stage: string
  progress: number
  message: string
}

export interface ProjectHistoryItem {
  batchNo: string
  kind: 'deploy' | 'rollback' | string
  projectVersion: string
  packageId: number
  packageName: string
  packageSize: number
  sha256: string
  operator: string
  remark: string
  status: 'running' | 'success' | 'failed' | 'canceled' | string
  createdAt: string
  tasks: ProjectTaskBrief[]
}

export interface PublishResult {
  batchNo: string
  version: string
  tasks: Task[]
}

const get = <T>(url: string, params?: Record<string, unknown>) =>
  http.get(url, { params }) as unknown as Promise<T>
const post = <T>(url: string, data?: unknown, config?: Record<string, unknown>) =>
  http.post(url, data, config) as unknown as Promise<T>
const put = <T>(url: string, data?: unknown) => http.put(url, data) as unknown as Promise<T>
const del = <T>(url: string) => http.delete(url) as unknown as Promise<T>

export const authApi = {
  login: (data: { username: string; password: string; otp?: string }) =>
    post<{ token: string; user: User }>('/auth/login', data),
  me: () => get<User>('/auth/me'),
  changePassword: (data: { oldPassword: string; newPassword: string; otp?: string }) =>
    post<{ updated: boolean }>('/auth/password', data),
  otpSetup: () => post<{ secret: string; otpauth: string; issuer: string; period: number }>('/auth/otp/setup'),
  otpActivate: (code: string) => post<{ enabled: boolean; recoveryCodes?: string[] }>('/auth/otp/activate', { code }),
  otpRebind: (code: string) =>
    post<{ secret: string; otpauth: string; issuer: string; period: number }>('/auth/otp/rebind', { code }),
  otpDisable: (code: string) => post<{ enabled: boolean }>('/auth/otp/disable', { code }),
  recoveryStatus: () => get<{ total: number; remaining: number }>('/auth/recovery-codes'),
  regenerateRecoveryCodes: (code: string) =>
    post<{ recoveryCodes: string[] }>('/auth/recovery-codes/regenerate', { code }),
}

export const serverApi = {
  list: () => get<Server[]>('/servers'),
  create: (data: Partial<Server>) => post<Server>('/servers', data),
  update: (id: number, data: Partial<Server>) => put<Server>(`/servers/${id}`, data),
  remove: (id: number) => del<{ deleted: boolean }>(`/servers/${id}`),
  ping: (id: number) => post<{ status: string; message: string; mode: string }>(`/servers/${id}/ping`),
  /** 即时采集运行指标（负载/内存/磁盘 + 凭据自检） */
  probe: (id: number) => post<ProbeResult>(`/servers/${id}/probe`),
  /** 采集目标机当前主机指纹（不改变登记状态） */
  probeHostKey: (id: number) => post<HostKeyInfo>(`/servers/${id}/hostkey/probe`),
  /** 清除已登记的主机指纹，下次连接重新自动信任（服务器重建场景） */
  clearHostKey: (id: number) => del<{ cleared: boolean }>(`/servers/${id}/hostkey`),
}

export const directoryApi = {
  list: (serverId?: number) => get<Directory[]>('/directories', serverId ? { serverId } : undefined),
  create: (data: Partial<Directory>) => post<Directory>('/directories', data),
  update: (id: number, data: Partial<Directory>) => put<Directory>(`/directories/${id}`, data),
  remove: (id: number) => del<{ deleted: boolean }>(`/directories/${id}`),
}

export const scriptApi = {
  list: () => get<Script[]>('/scripts'),
  create: (data: Partial<Script>) => post<Script>('/scripts', data),
  update: (id: number, data: Partial<Script>) => put<Script>(`/scripts/${id}`, data),
  remove: (id: number) => del<{ deleted: boolean }>(`/scripts/${id}`),
}

/** 单包上限，与后端 maxPackageSize、Nginx client_max_body_size 保持一致 */
export const MAX_PACKAGE_BYTES = 200 * 1024 * 1024

/** 生成本次上传的幂等令牌：同一文件的自动重试携带同一令牌，服务端命中后直接返回原记录 */
function newUploadToken(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

function uploadOnce(file: File, token: string, onProgress?: (pct: number) => void) {
  const form = new FormData()
  form.append('file', file)
  return post<Package>('/packages', form, {
    headers: { 'Content-Type': 'multipart/form-data', 'X-Upload-Token': token },
    // 全局 60s 超时会在上传大包（上限 200MB）中途中断请求，上传单独放宽到 15 分钟。
    timeout: 15 * 60 * 1000,
    onUploadProgress: (e: ProgressEvent) => {
      if (onProgress && e.total) {
        onProgress(Math.round((e.loaded / e.total) * 100))
      }
    },
  })
}

export const packageApi = {
  list: () => get<Package[]>('/packages'),
  remove: (id: number) => del<{ deleted: boolean }>(`/packages/${id}`),
  /**
   * 上传安装包。网络中断、超时、网关错误自动重试一次（大包走公网/隧道时很常见）。
   * 服务端采用「临时文件 + fsync + 原子改名」，失败不会留下半截包文件；
   * 重试携带同一幂等令牌，服务端命中后直接返回已登记的记录，不会产生重复条目。
   */
  upload: async (
    file: File,
    onProgress?: (pct: number) => void,
    options?: { retries?: number; onRetry?: (attempt: number) => void },
  ): Promise<Package> => {
    const retries = options?.retries ?? 1
    const token = newUploadToken()
    for (let attempt = 0; ; attempt++) {
      try {
        return await uploadOnce(file, token, onProgress)
      } catch (e: any) {
        if (attempt >= retries || !e?.retryable) throw e
        options?.onRetry?.(attempt + 1)
        onProgress?.(0)
        await new Promise((resolve) => setTimeout(resolve, 600 * (attempt + 1)))
      }
    }
  },
}

export const taskApi = {
  list: (params: Record<string, unknown>) => get<Page<Task>>('/tasks', params),
  get: (id: number) => get<{ task: Task; steps: TaskStep[] }>(`/tasks/${id}`),
  create: (data: {
    serverId: number
    directoryId: number
    packageId: number
    preScriptId?: number
    scriptId?: number
    otp?: string
  }) => post<Task>('/tasks', data),
  cancel: (id: number) => post<{ canceled: boolean }>(`/tasks/${id}/cancel`),
  remove: (id: number) => del<{ deleted: boolean }>(`/tasks/${id}`),
}

export const auditApi = {
  list: (params: Record<string, unknown>) => get<Page<AuditLog>>('/audit', params),
}

export const userApi = {
  list: () => get<User[]>('/users'),
  create: (data: Record<string, unknown>) => post<User>('/users', data),
  update: (id: number, data: Record<string, unknown>) => put<User>(`/users/${id}`, data),
  remove: (id: number) => del<{ deleted: boolean }>(`/users/${id}`),
  resetOtp: (id: number) => post<{ reset: boolean }>(`/users/${id}/otp/reset`),
}

export const projectApi = {
  list: () => get<Project[]>('/projects'),
  get: (id: number) => get<Project>(`/projects/${id}`),
  create: (data: { name: string; code: string; description?: string; scriptId?: number }) => post<Project>('/projects', data),
  update: (
    id: number,
    data: {
      name?: string
      description?: string
      scriptId?: number
      directoryIds?: number[]
      memberIds?: number[]
      policy?: {
        backupBeforeDeploy?: boolean
        allowOverwrite?: boolean
        keepReleases?: number
      }
    },
  ) => put<Project>(`/projects/${id}`, data),
  remove: (id: number) => del<{ deleted: boolean }>(`/projects/${id}`),
  history: (id: number) => get<ProjectHistoryItem[]>(`/projects/${id}/history`),
  publish: (id: number, data: { packageId: number; preScriptId?: number; scriptId?: number; otp?: string }) =>
    post<PublishResult>(`/projects/${id}/publish`, data),
  rollback: (id: number, data: { batchNo: string; otp?: string }) =>
    post<PublishResult>(`/projects/${id}/rollback`, data),
}

export const overviewApi = {
  get: () => get<Overview>('/overview'),
}
