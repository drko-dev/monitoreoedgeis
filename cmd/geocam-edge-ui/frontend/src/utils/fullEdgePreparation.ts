import type { FullEdgePreparationSnapshot } from '../types/installer';
import type { TranslationKey } from '../i18n/translations';

export const preparationStages = [
  'NOT_PREPARED',
  'CHECKING',
  'PREPARING',
  'VERIFYING',
  'READY',
] as const;

const sha256Pattern = /^[a-f0-9]{64}$/i;
const requiredComponents = new Set([
  'python', 'wheelhouse', 'worker', 'ffmpeg', 'ffprobe', 'authorized_models',
]);

const preparationStates = new Set([
  'NOT_PREPARED',
  'CHECKING',
  'PREPARING',
  'VERIFYING',
  'READY',
  'RECOVERY_REQUIRED',
  'FAILED',
  'CANCELLED',
]);

function nonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

export function isFullEdgePreparationSnapshot(value: unknown): value is FullEdgePreparationSnapshot {
  if (!value || typeof value !== 'object') return false;
  const snapshot = value as Partial<FullEdgePreparationSnapshot>;
  return typeof snapshot.state === 'string' && preparationStates.has(snapshot.state) &&
    Number.isInteger(snapshot.progress_percent) && snapshot.progress_percent! >= 0 && snapshot.progress_percent! <= 100 &&
    typeof snapshot.can_start === 'boolean' && typeof snapshot.retryable === 'boolean' &&
    typeof snapshot.cancellation_allowed === 'boolean' && typeof snapshot.recovery_available === 'boolean' &&
    typeof snapshot.diagnostics_available === 'boolean';
}

export function isCertifiedReady(snapshot: FullEdgePreparationSnapshot | null): boolean {
  if (!snapshot || snapshot.state !== 'READY' || snapshot.progress_percent !== 100) return false;
  const certificate = snapshot.certification;
  if (!certificate || typeof certificate !== 'object') return false;

  const certificateValid = Boolean(
    nonEmptyString(certificate.certification_id) &&
    nonEmptyString(certificate.runtime_pack_version) &&
    typeof certificate.runtime_manifest_sha256 === 'string' && sha256Pattern.test(certificate.runtime_manifest_sha256) &&
    nonEmptyString(certificate.platform) &&
    certificate.evidence_level === 'verified' &&
    certificate.runtime_pack_verified === true &&
    certificate.dependencies_verified === true &&
    certificate.vision_worker_ready === true &&
    certificate.ffmpeg_verified === true &&
    certificate.ffprobe_verified === true &&
    nonEmptyString(certificate.model_pack_version) &&
    typeof certificate.model_manifest_sha256 === 'string' && sha256Pattern.test(certificate.model_manifest_sha256) &&
    certificate.model_authorized === true &&
    certificate.model_integrity_verified === true &&
    certificate.inference_smoke_test_passed === true &&
    typeof certificate.certified_at === 'string' && Number.isFinite(Date.parse(certificate.certified_at)),
  );

  if (!certificateValid) return false;

  // Require exactly one READY report per component; missing, duplicate, or
  // unknown keys must never promote a runtime to READY.
  if (!Array.isArray(snapshot.components) || snapshot.components.length !== requiredComponents.size) return false;
  const reportedComponents = new Set<string>();
  for (const comp of snapshot.components) {
    if (!requiredComponents.has(comp.key) || comp.status !== 'READY' || reportedComponents.has(comp.key)) return false;
    reportedComponents.add(comp.key);
  }
  return reportedComponents.size === requiredComponents.size;
}

export function isPreparationActive(state: string | undefined): boolean {
  return state === 'CHECKING' || state === 'PREPARING' || state === 'VERIFYING';
}

export function canStartPreparation(snapshot: FullEdgePreparationSnapshot | null): boolean {
  if (!snapshot || isPreparationActive(snapshot.state) || isCertifiedReady(snapshot)) return false;
  return snapshot.can_start === true && (
    snapshot.state === 'NOT_PREPARED' ||
    (snapshot.state === 'FAILED' && snapshot.retryable) ||
    snapshot.state === 'CANCELLED'
  );
}

export function canCancelPreparation(snapshot: FullEdgePreparationSnapshot | null): boolean {
  return Boolean(snapshot?.cancellation_allowed === true && isPreparationActive(snapshot.state));
}

export function canRecoverPreparation(snapshot: FullEdgePreparationSnapshot | null): boolean {
  return Boolean(snapshot?.recovery_available === true && snapshot.state === 'RECOVERY_REQUIRED');
}

export function preparationErrorKey(errorCode?: string): TranslationKey {
  switch (errorCode) {
    case 'HASH_MISMATCH': return 'preparation.errorHashMismatch';
    case 'DEPENDENCY_MISSING': return 'preparation.errorDependencyMissing';
    case 'INSUFFICIENT_DISK': return 'preparation.errorDiskSpace';
    case 'ARCHITECTURE_UNSUPPORTED': return 'preparation.errorArchitecture';
    case 'LICENSE_NOT_APPROVED': return 'preparation.errorLicense';
    case 'MODEL_UNAUTHORIZED': return 'preparation.errorModelUnauthorized';
    case 'PREPARATION_NOT_INTEGRATED': return 'preparation.notIntegrated';
    case 'RUNTIME_UNCERTIFIED': return 'preparation.runtimeInstalledUncertified';
    case 'RECOVERY_PENDING': return 'preparation.recoveryPending';
    case 'CANCELLED': return 'preparation.cancelled';
    default: return 'preparation.errorGeneric';
  }
}

export function isProgressValid(snapshot: FullEdgePreparationSnapshot): boolean {
  return Number.isInteger(snapshot.progress_percent) && snapshot.progress_percent >= 0 && snapshot.progress_percent <= 100;
}

const stackTraceMarker = /(?:^\s*at\s+|goroutine \d+|Traceback \(most recent call last\)|\n\s*File \u0022|panic:)/i;
const sensitiveValue = /(?:password|passwd|token|secret|authorization|bearer)\s*[:=]\s*\S+/ig;
const absolutePath = /(?:\/Users\/|\/home\/|\/private\/|\/var\/|\/tmp\/)\S+/g;

// Diagnostics remain useful but must not leak stack traces, host paths, or secrets.
export function safeDiagnosticText(value: string | undefined, fallback: string): string {
  if (!value || stackTraceMarker.test(value) || /[\r\n\t]/.test(value)) return fallback;
  return value
    .replace(/[\r\n\t]+/g, ' ')
    .replace(sensitiveValue, '[REDACTED]')
    .replace(absolutePath, '[PATH]')
    .slice(0, 240);
}
