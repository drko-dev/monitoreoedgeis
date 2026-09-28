import { useState, useCallback } from 'react';
import {
  OnboardingCandidate,
  CameraValidationResult,
  CameraOnboardingPlan,
  CameraOnboardingApplyResult,
  SafeError,
} from '../types/installer';
import {
  discoverCameras,
  testCameraCredentials,
  planCameraOnboarding,
  applyCameraOnboarding,
  cancelCameraOnboarding,
} from '../services/api';

export type ScanState = 'IDLE' | 'SCANNING' | 'FOUND' | 'NONE_FOUND' | 'ERROR';

export function useCameraOnboarding() {
  const [scanState, setScanState] = useState<ScanState>('IDLE');
  const [candidates, setCandidates] = useState<OnboardingCandidate[]>([]);
  const [scanError, setScanError] = useState<string | null>(null);

  const [validating, setValidating] = useState(false);
  const [validation, setValidation] = useState<CameraValidationResult | null>(null);

  const [planning, setPlanning] = useState(false);
  const [plan, setPlan] = useState<CameraOnboardingPlan | null>(null);

  const [applying, setApplying] = useState(false);
  const [applyResult, setApplyResult] = useState<CameraOnboardingApplyResult | null>(null);
  const [applyError, setApplyError] = useState<SafeError | null>(null);

  const scan = useCallback(async () => {
    setScanState('SCANNING');
    setScanError(null);
    try {
      const result = await discoverCameras();
      setCandidates(result.candidates);
      setScanState(result.candidates.length > 0 ? 'FOUND' : 'NONE_FOUND');
    } catch (err: unknown) {
      setScanError(err instanceof Error ? err.message : 'Discovery scan failed');
      setScanState('ERROR');
    }
  }, []);

  const validate = useCallback(async (candidateKey: string, username: string, password: string) => {
    setValidating(true);
    setValidation(null);
    try {
      const result = await testCameraCredentials({ candidate_key: candidateKey, username, password });
      setValidation(result);
      return result;
    } finally {
      setValidating(false);
    }
  }, []);

  const requestPlan = useCallback(
    async (candidateKey: string, cameraName: string, manufacturer: string, model: string, username: string, password: string) => {
      setPlanning(true);
      setPlan(null);
      try {
        const p = await planCameraOnboarding({
          candidate_key: candidateKey,
          camera_name: cameraName,
          manufacturer,
          model,
          username,
          password,
        });
        setPlan(p);
        return p;
      } finally {
        setPlanning(false);
      }
    },
    [],
  );

  const apply = useCallback(
    async (candidateKey: string, cameraName: string, manufacturer: string, model: string, username: string, password: string) => {
      setApplying(true);
      setApplyError(null);
      setApplyResult(null);
      try {
        const result = await applyCameraOnboarding({
          candidate_key: candidateKey,
          camera_name: cameraName,
          manufacturer,
          model,
          username,
          password,
        });
        setApplyResult(result);
        return result;
      } catch (err: unknown) {
        const safeErr =
          err && typeof err === 'object' && 'safe_message' in err
            ? (err as SafeError)
            : { code: 'UNKNOWN_ERROR', safe_message: 'An unexpected error occurred while adding the camera.', recoverable: true };
        setApplyError(safeErr);
        throw safeErr;
      } finally {
        setApplying(false);
      }
    },
    [],
  );

  const cancel = useCallback(async (operationId: number) => {
    await cancelCameraOnboarding(operationId);
  }, []);

  const reset = useCallback(() => {
    setValidation(null);
    setPlan(null);
    setApplyResult(null);
    setApplyError(null);
  }, []);

  return {
    scanState,
    candidates,
    scanError,
    scan,
    validating,
    validation,
    validate,
    planning,
    plan,
    requestPlan,
    applying,
    applyResult,
    applyError,
    apply,
    cancel,
    reset,
  };
}
