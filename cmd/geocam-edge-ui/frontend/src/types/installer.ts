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

// Processing mode (UX-3). Exactly three product modes; "gateway" is
// deliberately not a fourth mode here -- see
// docs/product/UX3_PROCESSING_MODE_CONFIGURATION.md.
export type ProcessingMode = 'cloud' | 'hybrid' | 'full_edge';

export type CapabilityStatus = 'SUPPORTED' | 'SUPPORTED_WITH_WARNINGS' | 'UNAVAILABLE';

export interface ProcessingModeOption {
  mode: ProcessingMode;
  display_name: string;
  description: string;
  local_compute: string;
  network_dependency: string;
  inference_location: string;
  capability: CapabilityStatus;
  capability_reason?: string;
  warnings?: string[];
  blockers?: string[];
}

export interface CurrentProcessingMode {
  mode: ProcessingMode;
  pipeline_enabled: boolean;
  effective_profile: string;
  source: 'runtime' | 'config' | 'default';
}

export interface ProcessingModeRequest {
  mode: ProcessingMode;
}

export interface ProcessingModePlan {
  requested_mode: ProcessingMode;
  current_mode: ProcessingMode;
  current_effective_profile: string;
  target_effective_profile: string;
  config_changes: Record<string, string>;
  restart_required: boolean;
  components_required?: string[];
  warnings?: string[];
  blockers?: string[];
  rollback_available: boolean;
}

export type ApplyStatus = 'SUCCESS' | 'RESTART_REQUIRED' | 'ROLLED_BACK' | 'BLOCKED';

export interface ProcessingModeApplyResult {
  requested_product_mode: ProcessingMode;
  expected_processing_mode: string;
  expected_effective_profile: string;
  actual_processing_mode: string;
  actual_effective_profile: string;
  match: boolean;
  status: ApplyStatus;
  restart_required: boolean;
  rolled_back: boolean;
  safe_message: string;
  warnings?: string[];
}

