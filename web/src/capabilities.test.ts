import { describe, expect, it } from 'vitest'
import { describeCapabilities, describeCapability } from './capabilities'
import { setLocale } from './i18n'

describe('Localized capability explanations', () => {
  it.each([
    ['RESTIC_MISSING', 'missingTool', '缺少工具', 'DM_RESTIC_PATH'],
    ['DOCKER_CLI_MISSING', 'missingTool', '缺少工具', 'DM_COMPOSE_PATH'],
    ['COMPOSE_V2_UNAVAILABLE', 'missingTool', '缺少工具', 'docker compose version'],
    ['HOST_TOOL_MISSING', 'missingTool', '缺少工具', 'systemctl'],
    ['ALLOWED_PATHS_MISSING', 'notConfigured', '未配置', 'DM_ALLOWED_PATHS'],
    ['PATH_MAPPINGS_MISSING', 'notConfigured', '未配置', 'DM_PATH_MAPPINGS'],
    ['LOCAL_DOCKER_ENDPOINT_REQUIRED', 'notConfigured', '未配置', 'unix:///var/run/docker.sock'],
    ['DAEMON_CONFIG_PATH_INVALID', 'notConfigured', '未配置', 'daemon_config_path'],
    ['DAEMON_CONFIG_NOT_REGULAR', 'notConfigured', '未配置', '符号链接'],
    ['DAEMON_CONFIG_DIRECTORY_MISSING', 'notConfigured', '未配置', '/etc/docker'],
    ['DAEMON_WRITE_NOT_AUTHORIZED', 'notAuthorized', '未授权', '管理员密码'],
    ['HOST_PERMISSION_DENIED', 'notAuthorized', '未授权', '/etc/docker'],
    ['DAEMON_CONFIG_NOT_WRITABLE', 'notAuthorized', '未授权', '/etc/docker'],
    ['DOCKER_UNAVAILABLE', 'connectionError', '连接异常', 'Socket'],
    ['HOST_MODE_UNSUPPORTED', 'unsupported', '环境不支持', '容器模式下开启授权开关不会生效'],
    ['HOST_OS_UNSUPPORTED', 'unsupported', '环境不支持', 'Linux'],
    ['DAEMON_CONFIG_INSPECTION_FAILED', 'unknown', '需要排查', '服务日志'],
  ])('explains %s without relying on the raw English reason', (code, state, status, solution) => {
    const raw = 'arbitrary backend detail that must remain available for diagnosis'
    const item = describeCapability('backup.local', { available: false, reason_code: code, reason: raw, reason_params: { tool: 'systemctl', path: '/etc/docker' } })
    expect(item.name).toBe('本地备份与恢复')
    expect(item.state).toBe(state)
    expect(item.status).toBe(status)
    expect(item.solution).toContain(solution)
    expect(item.reason).not.toContain(raw)
    expect(item.rawReason).toBe(raw)
    expect(item.shortReason).not.toContain(raw)
    expect(item.shortReason).not.toMatch(/capabilities\./)
    expect(item.shortReason.length).toBeLessThan(24)
  })

  it('labels all six known functions in meaningful order and follows the current language', () => {
    const values = Object.fromEntries(['host.service_restart','backup.local','backup.bind_mount','compose.manage','host.daemon_config','docker.manage'].map(key => [key, { available: true }]))
    expect(describeCapabilities(values).map(item => item.name)).toEqual(['Docker 资源管理','Compose 项目管理','本地备份与恢复','目录备份与恢复','Docker 配置修改','Docker 服务重启'])
    setLocale('en', false)
    expect(describeCapability('backup.local', { available: false, reason_code: 'RESTIC_MISSING' })).toMatchObject({ name: 'Local backup and recovery', status: 'Missing tool' })
    expect(describeCapability('backup.local', { available: false, reason_code: 'RESTIC_MISSING' }).solution).toContain('Restic 0.19.1')
  })

  it('does not invent authorization states for unknown or legacy diagnostics', () => {
    for (const reason_code of [undefined, 'FUTURE_REASON']) {
      const item = describeCapability('future.feature', { available: false, reason_code, reason: 'raw future detail' })
      expect(item).toMatchObject({ key: 'future.feature', state: 'unknown', name: '其他功能', status: '需要排查', rawReason: 'raw future detail' })
      expect(item.solution).toContain('原始诊断信息')
    }
  })

  it('uses backend availability rather than a stale reason code and handles omitted parameters', () => {
    expect(describeCapability('docker.manage', { available: true, reason_code: 'DOCKER_UNAVAILABLE' })).toMatchObject({ available: true, state: 'available', status: '可用', solution: '', shortReason: '—' })
    expect(describeCapability('host.daemon_config', { available: false, reason_code: 'HOST_PERMISSION_DENIED' }).reason).toContain('相关配置目录或文件')
  })
})
