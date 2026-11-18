type TagType = 'success' | 'info' | 'warning' | 'danger' | 'primary'

const map: Record<string, { label: string; type: TagType }> = {
  pending: { label: '排队中', type: 'info' },
  uploading: { label: '上传中', type: 'primary' },
  verifying: { label: '校验中', type: 'warning' },
  executing: { label: '执行中', type: 'warning' },
  success: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'danger' },
  canceled: { label: '已取消', type: 'info' },
}

export function statusMeta(status: string) {
  return map[status] || { label: status || '未知', type: 'info' as TagType }
}

export function envMeta(env: string) {
  switch (env) {
    case 'prod':
      return { label: '生产', type: 'danger' as TagType }
    case 'test':
      return { label: '测试', type: 'warning' as TagType }
    default:
      return { label: '开发', type: 'info' as TagType }
  }
}

export function isRunning(status: string) {
  return ['pending', 'uploading', 'verifying', 'executing'].includes(status)
}

export function formatSize(bytes: number) {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let n = bytes
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}

/** ISO 时间转 YYYY-MM-DD HH:mm，供列表紧凑展示 */
export function formatDateTime(iso: string | undefined | null) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 解析指标快照中的目录占用 JSON（脏数据时返回空数组） */
export function parseDirUsage(dirUsage: string | undefined | null): { path: string; totalMB: number; usedMB: number; pct: number }[] {
  if (!dirUsage) return []
  try {
    const list = JSON.parse(dirUsage)
    return Array.isArray(list) ? list : []
  } catch {
    return []
  }
}

/** 服务器状态标签：优先用指标快照的结果，兼容仅 ping 过的旧数据 */
export function serverStatusMeta(status: string) {
  if (status === 'online') return { label: '在线', type: 'success' as TagType }
  if (status === 'offline') return { label: '不可达', type: 'danger' as TagType }
  return { label: '未检测', type: 'info' as TagType }
}

/** 秒数转可读运行时长 */
export function formatUptime(sec: number) {
  if (!sec || sec <= 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分钟`
}

/** 内存占用百分比 */
export function memPct(m: { memTotalMB: number; memUsedMB: number } | null | undefined): number | null {
  if (!m || m.memTotalMB <= 0) return null
  return Math.min(100, Math.round((m.memUsedMB / m.memTotalMB) * 100))
}

/** 根分区磁盘占用百分比（dirUsage 第一项即根目录） */
export function rootDiskPct(m: { dirUsage?: string } | null | undefined): number | null {
  const usages = parseDirUsage(m?.dirUsage)
  if (!usages.length) return null
  return Math.round(usages[0].pct)
}

/** 一分钟负载相对 CPU 核数的占用百分比（负载可超过核数，调用方自行截断） */
export function loadPct(m: { load1: number; cpuCores: number } | null | undefined): number | null {
  if (!m || m.cpuCores <= 0) return null
  return Math.round((m.load1 / m.cpuCores) * 1000) / 10
}

/** 资源水位分级：>=90% 危险，>=75% 警告，其余正常 */
export function usageLevel(pct: number | null | undefined): 'none' | 'ok' | 'warn' | 'danger' {
  if (pct == null) return 'none'
  if (pct >= 90) return 'danger'
  if (pct >= 75) return 'warn'
  return 'ok'
}

/** 资源水位配色，供环形图/进度条使用 */
export function usageColor(pct: number | null | undefined): string {
  switch (usageLevel(pct)) {
    case 'danger':
      return '#d64545'
    case 'warn':
      return '#e6a23c'
    case 'ok':
      return '#2f6bff'
    default:
      return '#c3c9d6'
  }
}
