import type { MessageSchema } from './common'
export const dashboardConsoleZh = {
  stopped: '已停止', cpu: 'CPU 使用率', memory: '内存使用量', disk: '磁盘使用量',
  collecting: '正在积累趋势', sessionTrend: '当前会话趋势 · 每 15 秒采样',
  unavailable: '暂时无法读取此指标', cpuWarming: '正在采集 CPU 基准，约 15 秒后可用', cpuUnsupported: '宿主机 CPU 需在 Linux 二进制部署中通过本地 Unix 套接字连接 Docker 后采集', cpuStale: 'CPU 采样暂不可用，等待下次采样',
  containers: '容器概览', allContainers: '全部容器', search: '搜索容器', searchPlaceholder: '搜索名称或镜像…',
  name: '名称', status: '状态', image: '镜像', ports: '端口', created: '创建时间', actions: '操作', view: '查看',
  noContainers: '暂无容器', noMatches: '未找到匹配的容器', filteredCount: '匹配 {count} 条 · 最多显示 5 条',
  containerError: '容器列表读取失败', taskError: '最近任务读取失败', storageError: '浏览器无法保存详情跳转信息，请从容器页面查看。',
}
export const dashboardConsoleEn: MessageSchema<typeof dashboardConsoleZh> = {
  stopped: 'Stopped', cpu: 'CPU usage', memory: 'Memory usage', disk: 'Disk usage',
  collecting: 'Collecting trend samples', sessionTrend: 'Current session · sampled every 15s',
  unavailable: 'This metric is currently unavailable', cpuWarming: 'Collecting CPU baseline; available in about 15s', cpuUnsupported: 'Host CPU sampling requires a Linux binary deployment connected to Docker through a local Unix socket', cpuStale: 'CPU sample unavailable; waiting for the next sample',
  containers: 'Container overview', allContainers: 'All containers', search: 'Search containers', searchPlaceholder: 'Search names or images…',
  name: 'Name', status: 'Status', image: 'Image', ports: 'Ports', created: 'Created', actions: 'Actions', view: 'View',
  noContainers: 'No containers yet', noMatches: 'No matching containers', filteredCount: '{count} matches · showing up to 5',
  containerError: 'Could not load containers', taskError: 'Could not load recent tasks', storageError: 'Your browser could not save the detail link. Open the container page to view details.',
}
