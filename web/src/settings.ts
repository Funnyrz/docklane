export interface PathMapping { host: string; local: string }
export interface CapabilityConfiguration {
  allowed_paths: string[]
  path_mappings: PathMapping[]
  enable_daemon_write: boolean
}
export interface CapabilitySettingsState {
  effective: CapabilityConfiguration
  saved: CapabilityConfiguration
  pending_restart: boolean
  daemon_toggle_supported: boolean
}

export function configurationFromForm(paths: string, mappings: PathMapping[], enableDaemon: boolean): CapabilityConfiguration {
  return {
    allowed_paths: paths.split('\n').map(path => path.trim()).filter(Boolean),
    path_mappings: mappings.map(mapping => ({ host: mapping.host.trim(), local: mapping.local.trim() })),
    enable_daemon_write: enableDaemon,
  }
}
