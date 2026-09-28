import React from 'react';
import { OnboardingCandidate } from '../types/installer';
import { ScanState } from '../hooks/useCameraOnboarding';
import { Button } from './Button';

interface CameraDiscoveryProps {
  scanState: ScanState;
  candidates: OnboardingCandidate[];
  scanError: string | null;
  onScan: () => void;
  onSelect: (candidate: OnboardingCandidate) => void;
}

export const CameraDiscovery: React.FC<CameraDiscoveryProps> = ({
  scanState,
  candidates,
  scanError,
  onScan,
  onSelect,
}) => {
  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">Add camera</h3>

      {scanState === 'IDLE' && (
        <>
          <p className="state-message">Search the local network for IP cameras.</p>
          <div className="actions-bar">
            <Button variant="primary" onClick={onScan}>
              Start scan
            </Button>
          </div>
        </>
      )}

      {scanState === 'SCANNING' && (
        <div className="loading-container">
          <div className="spinner" aria-hidden="true" />
          <p className="state-message">Searching local network...</p>
        </div>
      )}

      {scanState === 'ERROR' && (
        <>
          <p className="state-message">{scanError || 'Discovery scan failed.'}</p>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              Scan again
            </Button>
          </div>
        </>
      )}

      {scanState === 'NONE_FOUND' && (
        <>
          <p className="state-message">No cameras were found on the local network.</p>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              Scan again
            </Button>
          </div>
        </>
      )}

      {scanState === 'FOUND' && (
        <>
          <p className="card-header-row">Found devices</p>
          <ul className="check-list">
            {candidates.map((c) => (
              <li key={c.candidate_key} className="check-item">
                <div>
                  <span className="check-label">{c.model || c.manufacturer || c.host}</span>
                  <span className="check-value"> · {c.host}</span>
                  {c.multi_source && <span className="badge badge-neutral">DVR/NVR not supported</span>}
                  {!c.onvif_available && !c.multi_source && <span className="badge badge-warning">No ONVIF</span>}
                </div>
                <Button
                  variant="secondary"
                  onClick={() => onSelect(c)}
                  disabled={c.multi_source || !c.onvif_available}
                >
                  Select
                </Button>
              </li>
            ))}
          </ul>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              Scan again
            </Button>
          </div>
        </>
      )}
    </div>
  );
};
