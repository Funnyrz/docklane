import { t } from './i18n'

export interface Capability {
  available: boolean
  reason?: string
  reason_code?: string
  reason_params?: Record<string, string>
}

type CapabilityState = 'available' | 'missingTool' | 'notConfigured' | 'notAuthorized' | 'connectionError' | 'unsupported' | 'unknown'
const issues: Record<string, { state: CapabilityState; message: string }> = {
  DOCKER_UNAVAILABLE: { state: 'connectionError', message: 'dockerUnavailable' },
  DOCKER_CLI_MISSING: { state: 'missingTool', message: 'dockerCliMissing' },
  COMPOSE_V2_UNAVAILABLE: { state: 'missingTool', message: 'composeUnavailable' },
  RESTIC_MISSING: { state: 'missingTool', message: 'resticMissing' },
  ALLOWED_PATHS_MISSING: { state: 'notConfigured', message: 'allowedPathsMissing' },
  PATH_MAPPINGS_MISSING: { state: 'notConfigured', message: 'pathMappingsMissing' },
  HOST_OS_UNSUPPORTED: { state: 'unsupported', message: 'hostOsUnsupported' },
  HOST_MODE_UNSUPPORTED: { state: 'unsupported', message: 'hostModeUnsupported' },
  DAEMON_WRITE_NOT_AUTHORIZED: { state: 'notAuthorized', message: 'daemonNotAuthorized' },
  LOCAL_DOCKER_ENDPOINT_REQUIRED: { state: 'notConfigured', message: 'localEndpointRequired' },
  DAEMON_CONFIG_PATH_INVALID: { state: 'notConfigured', message: 'configPathInvalid' },
  DAEMON_CONFIG_NOT_REGULAR: { state: 'notConfigured', message: 'configNotRegular' },
  DAEMON_CONFIG_INSPECTION_FAILED: { state: 'unknown', message: 'configInspectionFailed' },
  DAEMON_CONFIG_DIRECTORY_MISSING: { state: 'notConfigured', message: 'configDirectoryMissing' },
  HOST_TOOL_MISSING: { state: 'missingTool', message: 'hostToolMissing' },
  HOST_PERMISSION_DENIED: { state: 'notAuthorized', message: 'hostPermissionDenied' },
  DAEMON_CONFIG_NOT_WRITABLE: { state: 'notAuthorized', message: 'configNotWritable' },
}
const names: Record<string, string> = {
  'docker.manage': 'dockerManage',
  'compose.manage': 'composeManage',
  'backup.local': 'backupLocal',
  'backup.bind_mount': 'backupDirectories',
  'host.daemon_config': 'daemonConfig',
  'host.service_restart': 'serviceRestart',
}
const order = Object.keys(names)

export function describeCapability(key: string, value: Capability) {
  const available = value.available === true
  const issue = issues[value.reason_code ?? ''] ?? { state: 'unknown' as const, message: 'unknown' }
  const state = available ? 'available' : issue.state
  const params = { tool: t('capabilities.requiredTool'), path: t('capabilities.requiredPath'), ...value.reason_params }
  return {
    key,
    name: t(`capabilities.names.${names[key] ?? 'unknown'}`),
    available,
    state,
    status: t(`capabilities.states.${state}`),
    tone: state === 'available' ? 'success' : state === 'connectionError' || state === 'unknown' ? 'danger' : state === 'unsupported' ? '' : 'warning',
    reason: available ? t('capabilities.ready') : t(`capabilities.reasons.${issue.message}`, params),
    solution: available ? '' : t(`capabilities.solutions.${issue.message}`, params),
    rawReason: value.reason,
    reasonCode: value.reason_code,
  }
}

export function describeCapabilities(values: Record<string, Capability>) {
  return Object.entries(values)
    .sort(([a], [b]) => (order.indexOf(a) < 0 ? order.length : order.indexOf(a)) - (order.indexOf(b) < 0 ? order.length : order.indexOf(b)))
    .map(([key, value]) => describeCapability(key, value))
}
