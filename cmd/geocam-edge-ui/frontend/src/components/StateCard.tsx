import React from 'react';
import { InstallerState } from '../types/installer';
import { useI18n } from '../i18n/I18nContext';
import { TranslationKey } from '../i18n/translations';

interface StateCardProps {
  state: InstallerState;
}

export const StateCard: React.FC<StateCardProps> = ({ state }) => {
  const { t } = useI18n();

  const getBadgeClass = () => {
    switch (state.state) {
      case 'ENROLLED':
        return 'badge-success';
      case 'NEEDS_ENROLLMENT':
      case 'NEW':
        return 'badge-info';
      case 'ACTION_REQUIRED':
        return 'badge-warning';
      case 'BLOCKED':
        return 'badge-danger';
      default:
        return 'badge-neutral';
    }
  };

  const getReadableStateName = () => {
    const key: Record<string, TranslationKey> = {
      NEEDS_ENROLLMENT: 'state.needsEnrollment',
      NEW: 'state.new',
      ENROLLED: 'state.enrolled',
      ACTION_REQUIRED: 'state.actionRequired',
      BLOCKED: 'state.blocked',
    };
    return key[state.state] ? t(key[state.state]) : state.state;
  };

  return (
    <section className="card" aria-labelledby="state-heading">
      <div className="card-header-row">
        <h2 id="state-heading" className="card-title">{t('state.title')}</h2>
        <span className={`badge ${getBadgeClass()}`}>{getReadableStateName()}</span>
      </div>
      <p className="state-message">{state.safe_message}</p>
      {state.device_id && (
        <div className="device-id-box">
          <span className="device-id-label">{t('state.deviceIdentifier')}</span>
          <code className="device-id-code">{state.device_id}</code>
        </div>
      )}
    </section>
  );
};
