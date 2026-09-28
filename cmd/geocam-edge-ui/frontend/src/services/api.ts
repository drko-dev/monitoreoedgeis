import {
  SystemReport,
  InstallerState,
  ClaimRequest,
  ClaimResult,
  ProcessingModeOption,
  CurrentProcessingMode,
  ProcessingModeRequest,
  ProcessingModePlan,
  ProcessingModeApplyResult,
  DiscoverCamerasResult,
  OnboardingCandidate,
  TestCameraCredentialsRequest,
  CameraValidationResult,
  CameraOnboardingRequest,
  CameraOnboardingPlan,
  CameraOnboardingApplyResult,
} from '../types/installer';
import {
  GetSystemReport,
  GetInstallerState,
  ClaimDevice,
  GetProcessingModeOptions,
  GetCurrentProcessingMode,
  PlanProcessingMode,
  ApplyProcessingMode,
  DiscoverCameras,
  GetDiscoveredCamera,
  TestCameraCredentials,
  PlanCameraOnboarding,
  ApplyCameraOnboarding,
  CancelCameraOnboarding,
} from '../../wailsjs/go/main/App';

// fetchSystemReport calls the Go facade via Wails binding
export async function fetchSystemReport(): Promise<SystemReport> {
  try {
    const report = await GetSystemReport();
    if (report && report.os) {
      return report as unknown as SystemReport;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }

  // Safe mock for standalone browser dev mode / headless unit tests
  return {
    os: 'macOS (Dev Sandbox)',
    arch: 'arm64',
    edge_version: 'v1.0.0-dev',
    commit: 'local',
    build_date: new Date().toISOString(),
    hostname: 'dev-workstation',
    privilege_level: 'STANDARD_USER',
    platform_supported: true,
    data_dir: '/var/lib/geocam-edge',
    service_installed: false,
    daemon_running: false,
    owns_instance_lock: false,
    config_present: false,
    enrolled: false,
    total_ram_mb: 16384,
    free_ram_mb: 8192,
    disk_path: '/',
    free_disk_gb: 120,
    ffmpeg_present: true,
    ffprobe_path: '/usr/local/bin/ffprobe',
    has_gpu_support: true,
    gpu_info: 'Apple Silicon Metal',
  };
}

// fetchInstallerState calls the Go facade via Wails binding
export async function fetchInstallerState(): Promise<InstallerState> {
  try {
    const state = await GetInstallerState();
    if (state && state.state) {
      return state as unknown as InstallerState;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }

  // Safe mock for standalone browser dev mode / headless unit tests
  return {
    state: 'NEEDS_ENROLLMENT',
    reason_code: 'DEVICE_NOT_ENROLLED',
    safe_message: 'Edge requires enrollment with GEO CAM SaaS.',
    recoverable: true,
    next_allowed_actions: ['PROCEED_TO_ENROLLMENT', 'REFRESH'],
  };
}

// claimDevice calls the Go facade to enroll the edge device with a one-time code
export async function claimDevice(req: ClaimRequest): Promise<ClaimResult> {
  try {
    const result = await ClaimDevice(req);
    if (result && result.device_id) {
      return result as unknown as ClaimResult;
    }
  } catch (err: unknown) {
    // Re-throw structured SafeError from Go facade
    if (err && typeof err === 'object' && 'code' in err) {
      throw err;
    }
    throw {
      code: 'NETWORK_ERROR',
      safe_message: 'Could not connect to SaaS server. Check your connection.',
      recoverable: true,
    };
  }

  // Safe mock for standalone browser dev mode / headless unit tests
  return {
    device_id: 'mock-device-id',
    organization_id: 1,
    device_kind: 'edge',
    status: 'active',
    edge_id: 'edge-dev-0001',
  };
}

const mockModeOptions: ProcessingModeOption[] = [
  {
    mode: 'cloud',
    display_name: 'Cloud',
    description: 'Inference runs in GEO CAM Cloud',
    local_compute: 'Lower',
    network_dependency: 'Higher',
    inference_location: 'Cloud',
    capability: 'SUPPORTED',
  },
  {
    mode: 'hybrid',
    display_name: 'Hybrid',
    description: 'Local motion/candidate filtering; inference runs in Cloud',
    local_compute: 'Moderate',
    network_dependency: 'Reduced',
    inference_location: 'Cloud',
    capability: 'SUPPORTED',
  },
  {
    mode: 'full_edge',
    display_name: 'Full Edge',
    description: 'Inference runs on this device',
    local_compute: 'Higher',
    network_dependency: 'Lowest',
    inference_location: 'Local device',
    capability: 'UNAVAILABLE',
    capability_reason: 'Local vision runtime is not configured (dev sandbox).',
    blockers: ['Local vision worker command is not configured.'],
  },
];

// fetchProcessingModeOptions calls the Go facade via Wails binding
export async function fetchProcessingModeOptions(): Promise<ProcessingModeOption[]> {
  try {
    const options = await GetProcessingModeOptions();
    if (Array.isArray(options)) {
      return options as unknown as ProcessingModeOption[];
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return mockModeOptions;
}

// fetchCurrentProcessingMode calls the Go facade via Wails binding
export async function fetchCurrentProcessingMode(): Promise<CurrentProcessingMode> {
  try {
    const current = await GetCurrentProcessingMode();
    if (current && current.mode) {
      return current as unknown as CurrentProcessingMode;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return {
    mode: 'cloud',
    pipeline_enabled: false,
    effective_profile: 'gateway-no-media',
    source: 'default',
  };
}

// planProcessingMode calls the Go facade to preview an Apply without mutating anything
export async function planProcessingMode(req: ProcessingModeRequest): Promise<ProcessingModePlan> {
  try {
    const plan = await PlanProcessingMode(req);
    if (plan && plan.requested_mode) {
      return plan as unknown as ProcessingModePlan;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return {
    requested_mode: req.mode,
    current_mode: 'cloud',
    current_effective_profile: 'gateway-no-media',
    target_effective_profile: req.mode === 'full_edge' ? 'full-edge' : req.mode === 'hybrid' ? 'hybrid' : 'gateway',
    config_changes: { GEOCAM_PROCESSING_MODE: req.mode === 'full_edge' ? 'edge' : req.mode, GEOCAM_VIDEO_PIPELINE_ENABLED: 'true' },
    restart_required: false,
    components_required: req.mode === 'full_edge' ? ['Local vision worker process', 'Person detection model', 'Vehicle detection model'] : undefined,
    rollback_available: true,
  };
}

// applyProcessingMode calls the Go facade to atomically persist a processing mode change
export async function applyProcessingMode(req: ProcessingModeRequest): Promise<ProcessingModeApplyResult> {
  try {
    return (await ApplyProcessingMode(req)) as unknown as ProcessingModeApplyResult;
  } catch (err: unknown) {
    if (err && typeof err === 'object' && 'code' in err) {
      throw err;
    }
    throw {
      code: 'NETWORK_ERROR',
      safe_message: 'Could not communicate with the Edge installer backend.',
      recoverable: true,
    };
  }
}

const mockCandidates: OnboardingCandidate[] = [
  {
    candidate_key: 'dev-mock-tc70',
    host: '192.168.0.42',
    manufacturer: 'TP-Link',
    model: 'Tapo TC70',
    onvif_available: true,
    auth_required: true,
    multi_source: false,
  },
];

// discoverCameras calls the Go facade to run one local network scan
export async function discoverCameras(): Promise<DiscoverCamerasResult> {
  try {
    const result = await DiscoverCameras();
    if (result && Array.isArray(result.candidates)) {
      return result as unknown as DiscoverCamerasResult;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return { candidates: mockCandidates, duration_ms: 0 };
}

// getDiscoveredCamera calls the Go facade to look up a previously discovered candidate
export async function getDiscoveredCamera(candidateKey: string): Promise<OnboardingCandidate> {
  try {
    const result = await GetDiscoveredCamera(candidateKey);
    if (result && result.candidate_key) {
      return result as unknown as OnboardingCandidate;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  const mock = mockCandidates.find((c) => c.candidate_key === candidateKey);
  if (mock) return mock;
  throw { code: 'CANDIDATE_NOT_FOUND', safe_message: 'This camera is no longer known. Run discovery again.', recoverable: true };
}

// testCameraCredentials calls the Go facade to validate a candidate over ONVIF + RTSP
export async function testCameraCredentials(req: TestCameraCredentialsRequest): Promise<CameraValidationResult> {
  try {
    const result = await TestCameraCredentials(req);
    if (result && result.candidate_key) {
      return result as unknown as CameraValidationResult;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return {
    candidate_key: req.candidate_key,
    multi_source: false,
    onvif_status: 'ok',
    rtsp_status: 'ok',
    profile: { codec: 'H264', width: 1920, height: 1080, fps: 15 },
    passed: true,
  };
}

// planCameraOnboarding calls the Go facade to preview an onboarding without mutating anything
export async function planCameraOnboarding(req: CameraOnboardingRequest): Promise<CameraOnboardingPlan> {
  try {
    const plan = await PlanCameraOnboarding(req);
    if (plan && plan.candidate_key) {
      return plan as unknown as CameraOnboardingPlan;
    }
  } catch {
    // Fallback below when not in Wails desktop runtime
  }
  return {
    candidate_key: req.candidate_key,
    camera_name: req.camera_name,
    profile: { codec: 'H264', width: 1920, height: 1080, fps: 15 },
    processing_mode: 'cloud',
    credentials_valid: true,
  };
}

// applyCameraOnboarding calls the Go facade to ask the SaaS to onboard the camera
export async function applyCameraOnboarding(req: CameraOnboardingRequest): Promise<CameraOnboardingApplyResult> {
  try {
    return (await ApplyCameraOnboarding(req)) as unknown as CameraOnboardingApplyResult;
  } catch (err: unknown) {
    if (err && typeof err === 'object' && 'code' in err) {
      throw err;
    }
    throw {
      code: 'NETWORK_ERROR',
      safe_message: 'Could not communicate with the Edge installer backend.',
      recoverable: true,
    };
  }
}

// cancelCameraOnboarding calls the Go facade to reverse a SaaS onboarding operation
export async function cancelCameraOnboarding(operationId: number): Promise<void> {
  try {
    await CancelCameraOnboarding(operationId);
  } catch {
    // Best-effort cleanup call; swallow errors in dev-sandbox/non-Wails mode
  }
}
