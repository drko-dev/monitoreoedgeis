import React from 'react';
import { ProcessingModeOption, ProcessingMode } from '../types/installer';
import { capabilityBadge } from '../utils/processingModeDisplay';
import { Button } from './Button';
import { HelpLink } from './HelpLink';
import { useI18n } from '../i18n/I18nContext';
import { docsLinks } from '../config/docsLinks';

interface ProcessingModeSelectorProps {
  options: ProcessingModeOption[];
  currentMode: ProcessingMode | null;
  selected: ProcessingMode | null;
  onSelect: (mode: ProcessingMode) => void;
  onContinue: () => void;
  loading: boolean;
}

export const ProcessingModeSelector: React.FC<ProcessingModeSelectorProps> = ({
  options,
  currentMode,
  selected,
  onSelect,
  onContinue,
  loading,
}) => {
  const { t } = useI18n();
  return (
    <div className="card mode-selector-card">
      <div className="card-header-row">
        <h3 className="card-title">{t('mode.chooseTitle')}</h3>
        <HelpLink href={docsLinks.processingModes}>{t('mode.whichShouldIChoose')}</HelpLink>
      </div>
      <div className="mode-card-grid">
        {options.map((option) => {
          const badge = capabilityBadge(option);
          const isUnavailable = option.capability === 'UNAVAILABLE';
          const isSelected = selected === option.mode;
          return (
            <button
              key={option.mode}
              type="button"
              className={`mode-card ${isSelected ? 'mode-card-selected' : ''} ${isUnavailable ? 'mode-card-disabled' : ''}`}
              onClick={() => !isUnavailable && onSelect(option.mode)}
              disabled={isUnavailable}
              aria-pressed={isSelected}
            >
              <div className="mode-card-header">
                <span className="mode-card-name">{option.display_name}</span>
                {currentMode === option.mode && <span className="badge badge-info">{t('mode.current')}</span>}
              </div>
              <p className="mode-card-description">{option.description}</p>
              <dl className="mode-card-facts">
                <div>
                  <dt>{t('mode.localCompute')}</dt>
                  <dd>{option.local_compute}</dd>
                </div>
                <div>
                  <dt>{t('mode.networkDependency')}</dt>
                  <dd>{option.network_dependency}</dd>
                </div>
                <div>
                  <dt>{t('mode.inference')}</dt>
                  <dd>{option.inference_location}</dd>
                </div>
              </dl>
              <span className={`badge ${badge.className}`}>{badge.label}</span>
              {option.capability_reason && <p className="mode-card-reason">{option.capability_reason}</p>}
              {isUnavailable && option.blockers && option.blockers.length > 0 && (
                <p className="mode-card-reason">{t('mode.reason', { reason: option.blockers[0] })}</p>
              )}
              {option.capability === 'SUPPORTED_WITH_WARNINGS' && option.warnings && option.warnings.length > 0 && (
                <p className="mode-card-reason">{option.warnings[0]}</p>
              )}
            </button>
          );
        })}
      </div>
      <div className="actions-bar">
        <Button variant="primary" onClick={onContinue} disabled={!selected} loading={loading}>
          {t('mode.continue')}
        </Button>
      </div>
    </div>
  );
};
