import React from 'react';
import { OnboardingCandidate } from '../types/installer';
import { ScanState } from '../hooks/useCameraOnboarding';
import { candidateSelectable, candidateChannelLabel } from '../utils/cameraOnboardingDisplay';
import { Button } from './Button';
import { HelpLink } from './HelpLink';
import { useI18n } from '../i18n/I18nContext';
import { docsLinks } from '../config/docsLinks';

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
  const { t } = useI18n();
  return (
    <div className="card mode-selector-card">
      <div className="card-header-row">
        <h3 className="card-title">{t('camera.discoveryTitle')}</h3>
        <HelpLink href={docsLinks.cameraDiscovery}>{t('camera.discoveryHelp')}</HelpLink>
      </div>

      {scanState === 'IDLE' && (
        <>
          <p className="state-message">{t('camera.searchLocalNetwork')}</p>
          <div className="actions-bar">
            <Button variant="primary" onClick={onScan}>
              {t('camera.startScan')}
            </Button>
          </div>
        </>
      )}

      {scanState === 'SCANNING' && (
        <div className="loading-container">
          <div className="spinner" aria-hidden="true" />
          <p className="state-message">{t('camera.scanning')}</p>
        </div>
      )}

      {scanState === 'ERROR' && (
        <>
          <p className="state-message">{scanError || t('camera.scanFailed')}</p>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              {t('camera.scanAgain')}
            </Button>
          </div>
        </>
      )}

      {scanState === 'NONE_FOUND' && (
        <>
          <p className="state-message">{t('camera.noneFound')}</p>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              {t('camera.scanAgain')}
            </Button>
          </div>
        </>
      )}

      {scanState === 'FOUND' && (
        <>
          <p className="card-header-row">{t('camera.foundDevices')}</p>
          <ul className="check-list">
            {candidates.map((c) => (
              <li key={c.candidate_key} className="check-item">
                <div>
                  <span className="check-label">{c.model || c.manufacturer || c.host}</span>
                  <span className="check-value"> · {c.host}</span>
                  {candidateChannelLabel(c) && (
                    <span className="badge badge-neutral">{candidateChannelLabel(c)}</span>
                  )}
                  {c.multi_source && !candidateChannelLabel(c) && (
                    <span className="badge badge-warning">{t('camera.channelUnresolved')}</span>
                  )}
                  {!c.onvif_available && <span className="badge badge-warning">{t('camera.noOnvif')}</span>}
                </div>
                <Button
                  variant="secondary"
                  onClick={() => onSelect(c)}
                  disabled={!candidateSelectable(c)}
                >
                  {t('camera.select')}
                </Button>
              </li>
            ))}
          </ul>
          <div className="actions-bar">
            <Button variant="secondary" onClick={onScan}>
              {t('camera.scanAgain')}
            </Button>
            <HelpLink href={docsLinks.dvrNvr}>{t('camera.dvrNvrHelp')}</HelpLink>
          </div>
        </>
      )}
    </div>
  );
};
