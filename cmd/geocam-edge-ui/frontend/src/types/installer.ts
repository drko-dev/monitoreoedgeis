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
  ffmpeg_path?: string;
  ffmpeg_version?: string;
  ffmpeg_source?: string;
  has_gpu_support: boolean;
  gpu_info?: string;
}

export type DependencySource = 'configured' | 'path' | 'homebrew' | 'system' | 'managed' | 'none';

export interface DependencyStatus {
  ffmpeg_found: boolean;
  ffmpeg_path: string;
  ffmpeg_version: string;
  ffprobe_found: boolean;
  ffprobe_path: string;
  ready: boolean;
  source: DependencySource;
  install_supported: boolean;
  install_method: string;
  message?: string;
}

export interface VisionRuntimeStatus {
  python_found: boolean;
  python_path: string;
  python_version: string;
  venv_found: boolean;
  venv_path: string;
  worker_script_path: string;
  packages_ready: boolean;
  torch_available: boolean;
  torch_version?: string;
  ultralytics_ready: boolean;
  ultralytics_version?: string;
  pillow_ready: boolean;
  device: string;
  device_name: string;
  models_dir: string;
  person_model_found: boolean;
  vehicle_model_found: boolean;
  missing_models?: string[];
  ready: boolean;
  can_provision: boolean;
  message?: string;
}

export type FullEdgePreparationState =
  | 'initial'
  | 'runtime_pack_detected'
  | 'preparation_pending'
  | 'verifying_integrity'
  | 'installing_dependencies'
  | 'preparing_worker'
  | 'verifying_media'
  | 'validating_model'
  | 'runtime_ready'
  | 'recoverable_error'
  | 'blocked_error'
  | 'cancelled'
  | 'recovery_pending'
  | 'recovering'
  | 'recovered';

export interface RuntimeCertification {
  certification_id: string;
  runtime_pack_version: string;
  runtime_manifest_sha256: string;
  platform: string;
  evidence_level: string;
  runtime_pack_verified: boolean;
  dependencies_verified: boolean;
  vision_worker_ready: boolean;
  ffmpeg_verified: boolean;
  ffprobe_verified: boolean;
  model_pack_version: string;
  model_manifest_sha256: string;
  model_authorized: boolean;
  model_integrity_verified: boolean;
  inference_smoke_test_passed: boolean;
  certified_at: string;
}

export interface FullEdgePreparationSnapshot {
  state: FullEdgePreparationState;
  stage?: string;
  progress_percent: number;
  operation_id?: string;
  can_start: boolean;
  retryable: boolean;
  cancellation_allowed: boolean;
  recovery_available: boolean;
  error_code?: string;
  safe_message?: string;
  diagnostics_available: boolean;
  certification?: RuntimeCertification;
}

export interface PreparationCheckResult {
  name: string;
  status: string;
  detail?: string;
}

export interface FullEdgePreparationDiagnostic {
  code: string;
  summary: string;
  checks?: PreparationCheckResult[];
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
  | 'INVALID_SAAS_URL'
  | 'SAAS_URL_REQUIRED'
  | 'CONFIG_WRITE_FAILED'
  | 'EDGE_ID_CONFLICT';

export interface ServerCheckResult {
  accessible: boolean;
  status: 'OK' | 'INVALID_URL' | 'INVALID_TLS' | 'UNREACHABLE';
  message: string;
  http_status?: number;
}

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

export interface CommissioningStatus {
  daemon_installed: boolean;
  service_registered: boolean;
  daemon_running: boolean;
  healthz_ok: boolean;
  readyz_ok: boolean;
  operational_state: string;
  operational_ok: boolean;
  saas_sync_ok: boolean;
  heartbeat_state: string;
  processing_mode: string;
  effective_profile: string;
  vision_worker_ready: boolean;
  camera_count: number;
  overall_ready: boolean;
  safe_message: string;
}

export type CommissionOutcome = 'SUCCESS' | 'ACTION_REQUIRED' | 'FAILED';

export interface CommissionResult {
  outcome: CommissionOutcome;
  status?: CommissioningStatus;
  safe_message: string;
  details?: string[];
}


