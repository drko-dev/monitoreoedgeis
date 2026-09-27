import { SystemReport, InstallerState } from '../types/installer';
import { GetSystemReport, GetInstallerState } from '../../wailsjs/go/main/App';

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
