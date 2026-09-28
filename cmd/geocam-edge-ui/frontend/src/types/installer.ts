export type StateCode =
  | 'NEW'
  | 'SYSTEM_CHECK'
  | 'NEEDS_ENROLLMENT'
  | 'ENROLLED'
  | 'ACTION_REQUIRED'
  | 'BLOCKED';

export type PrivilegeLevel =
  | 'STANDARD_USER'
  | 'ADMINISTRATOR'
  | 'ROOT'
  | 'UNKNOWN';

export interface SystemReport {
  os: string;
  arch: string;
  edge_version: string;
  commit: string;
  build_date: string;
  hostname: string;
  privilege_level: PrivilegeLevel;
  platform_supported: boolean;
  data_dir: string;
  service_installed: boolean;
  daemon_running: boolean;
  owns_instance_lock: boolean;
  config_present: boolean;
  enrolled: boolean;
  edge_id?: string;
  total_ram_mb: number;
  free_ram_mb: number;
  disk_path: string;
  free_disk_gb: number;
  ffmpeg_present: boolean;
  ffprobe_path?: string;
  has_gpu_support: boolean;
  gpu_info?: string;
}

export interface InstallerState {
  state: StateCode;
  reason_code: string;
  safe_message: string;
  recoverable: boolean;
  next_allowed_actions: string[];
  device_id?: string;
}

export interface SafeError {
  code: string;
  safe_message: string;
  recoverable: boolean;
  details?: string;
}

export interface ClaimRequest {
  code: string;
  device_name?: string;
  saas_url?: string;
}

export interface ClaimResult {
  device_id: string;
  organization_id: number;
  device_kind: string;
  status: string;
  edge_id: string;
}

export type EnrollmentStatus =
  | 'IDLE'
  | 'VALIDATING'
  | 'CLAIMING'
  | 'SUCCESS'
  | 'INVALID_CODE'
  | 'EXPIRED_CODE'
  | 'ALREADY_USED'
  | 'RATE_LIMITED'
  | 'NETWORK_ERROR'
  | 'SERVER_ERROR'
  | 'PERSISTENCE_ERROR'
  | 'EDGE_ID_CONFLICT';

