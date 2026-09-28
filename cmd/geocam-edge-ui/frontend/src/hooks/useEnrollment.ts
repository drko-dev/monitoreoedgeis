import { useState, useCallback } from 'react';
import { ClaimRequest, ClaimResult, SafeError, EnrollmentStatus } from '../types/installer';
import { claimDevice } from '../services/api';
import { useI18n } from '../i18n/I18nContext';

const CROCKFORD_CHARS = /^[0-9A-HJKMNP-TV-Z]+$/;
const CODE_LENGTH = 8;

/** Normalize Crockford Base32: strip formatting, uppercase, fix lookalikes */
function normalizeCode(raw: string): string {
  return raw
    .toUpperCase()
    .replace(/[-\s]/g, '')
    .replace(/O/g, '0')
    .replace(/[IL]/g, '1');
}

/** Validate a raw enrollment code string */
function validateCode(raw: string, t: ReturnType<typeof useI18n>['t']): string | null {
  const normalized = normalizeCode(raw);
  if (normalized.length !== CODE_LENGTH) {
    return t('enrollment.validationLength', { length: CODE_LENGTH });
  }
  if (!CROCKFORD_CHARS.test(normalized)) {
    return t('enrollment.validationChars');
  }
  return null;
}

/** Auto-format code as XXXX-XXXX while typing */
export function formatCodeInput(value: string): string {
  const clean = value.toUpperCase().replace(/[^0-9A-Z]/g, '');
  if (clean.length <= 4) return clean;
  return `${clean.slice(0, 4)}-${clean.slice(4, 8)}`;
}

export function useEnrollment() {
  const { t } = useI18n();
  const [code, setCode] = useState('');
  const [deviceName, setDeviceName] = useState('');
  const [status, setStatus] = useState<EnrollmentStatus>('IDLE');
  const [result, setResult] = useState<ClaimResult | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const handleCodeChange = useCallback((raw: string) => {
    setCode(formatCodeInput(raw));
    // Clear previous errors when user edits the code
    if (status !== 'IDLE' && status !== 'CLAIMING') {
      setStatus('IDLE');
      setErrorMessage(null);
    }
  }, [status]);

  const submit = useCallback(async () => {
    // Local validation first
    setStatus('VALIDATING');
    setErrorMessage(null);

    const validationError = validateCode(code, t);
    if (validationError) {
      setStatus('INVALID_CODE');
      setErrorMessage(validationError);
      return;
    }

    // Call backend
    setStatus('CLAIMING');
    const req: ClaimRequest = {
      code: normalizeCode(code),
      device_name: deviceName || undefined,
    };

    try {
      const claimResult = await claimDevice(req);
      setResult(claimResult);
      setStatus('SUCCESS');
    } catch (err: unknown) {
      const safe = err as SafeError;
      const errorCode = safe?.code || 'SERVER_ERROR';
      setStatus(errorCode as EnrollmentStatus);
      setErrorMessage(safe?.safe_message || t('enrollment.unexpectedError'));
    }
  }, [code, deviceName, t]);

  const reset = useCallback(() => {
    setCode('');
    setDeviceName('');
    setStatus('IDLE');
    setResult(null);
    setErrorMessage(null);
  }, []);

  return {
    code,
    deviceName,
    status,
    result,
    errorMessage,
    setCode: handleCodeChange,
    setDeviceName,
    submit,
    reset,
    canSubmit: code.replace(/[-\s]/g, '').length === CODE_LENGTH && status !== 'CLAIMING',
  };
}
