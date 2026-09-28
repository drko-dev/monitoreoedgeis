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

// Camera IP onboarding (UX-4). CandidateKey is always opaque: it comes from
// a DiscoverCameras/TestCameraCredentials response and is only ever echoed
// back, never constructed by the frontend.
export interface OnboardingCandidate {
  candidate_key: string;
  host: string;
  manufacturer?: string;
  model?: string;
  onvif_available: boolean;
  auth_required: boolean;
  multi_source: boolean;
  // Present only on a DVR/NVR expanded channel candidate (UX-6). A bare
  // multi-source device-level candidate never appears in DiscoverCameras
  // results -- discovery always expands straight to per-channel candidates
  // (or produces none, if no channel has a usable source token) -- so
  // multi_source === true implies these three fields are set.
  channel_label?: string;
  channel_index?: number;
  channel_count?: number;
}

export interface DiscoverCamerasResult {
  candidates: OnboardingCandidate[];
  duration_ms: number;
}

export interface TestCameraCredentialsRequest {
  candidate_key: string;
  username: string;
  password: string;
}

export interface StreamProfile {
  codec?: string;
  width?: number;
  height?: number;
  fps?: number;
}

export interface CameraValidationResult {
  candidate_key: string;
  multi_source: boolean;
  onvif_status: string;
  onvif_reason?: string;
  rtsp_status: string;
  rtsp_reason?: string;
  profile?: StreamProfile;
  passed: boolean;
}

export interface CameraOnboardingRequest {
  candidate_key: string;
  camera_name: string;
  manufacturer?: string;
  model?: string;
  username: string;
  password: string;
}

export interface CameraOnboardingPlan {
  candidate_key: string;
  camera_name: string;
  profile?: StreamProfile;
  processing_mode: string;
  credentials_valid: boolean;
  blockers?: string[];
  warnings?: string[];
}

export type CameraOnboardingStatus = 'SUCCESS' | 'ACTION_REQUIRED' | 'BLOCKED' | 'ROLLED_BACK';

export interface CameraOnboardingApplyResult {
  status: CameraOnboardingStatus;
  operation_id?: number;
  candidate_key: string;
  safe_message: string;
  sync_observed: boolean;
}

