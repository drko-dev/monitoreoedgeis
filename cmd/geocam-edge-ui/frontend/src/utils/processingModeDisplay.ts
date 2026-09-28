import type { ProcessingModeOption, ProcessingMode } from '../types/installer';

// Pure display-mapping helpers, kept out of .tsx files so they can be
// exercised directly by the plain node:test runner (which does not
// transform JSX) without needing a DOM or a JSX-aware test toolchain.

export const MODE_LABELS: Record<ProcessingMode, string> = {
  cloud: 'Cloud',
  hybrid: 'Hybrid',
  full_edge: 'Full Edge',
};

export interface CapabilityBadge {
  label: string;
  className: string;
}

export function capabilityBadge(option: Pick<ProcessingModeOption, 'capability'>): CapabilityBadge {
  switch (option.capability) {
    case 'SUPPORTED':
      return { label: 'Supported on this device', className: 'badge-success' };
    case 'SUPPORTED_WITH_WARNINGS':
      return { label: 'Available with warnings', className: 'badge-warning' };
    case 'UNAVAILABLE':
    default:
      return { label: 'Not available', className: 'badge-danger' };
  }
}

export function describeConfigKey(key: string): string {
  switch (key) {
    case 'GEOCAM_PROCESSING_MODE':
      return 'Processing engine';
    case 'GEOCAM_VIDEO_PIPELINE_ENABLED':
      return 'Local video pipeline';
    default:
      return key;
  }
}
