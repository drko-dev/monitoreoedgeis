import React from 'react';
import { useI18n } from '../i18n/I18nContext';

interface ErrorAlertProps {
  message: string;
  code?: string;
  onRetry?: () => void;
}

export const ErrorAlert: React.FC<ErrorAlertProps> = ({ message, code, onRetry }) => {
  const { t } = useI18n();
  return (
    <div className="alert alert-danger" role="alert">
      <div className="alert-content">
        <span className="alert-icon" aria-hidden="true">⚠️</span>
        <div>
          <h4 className="alert-title">{t('error.attentionRequired')}</h4>
          <p className="alert-message">{message}</p>
          {code && <span className="alert-code">{t('error.codePrefix', { code })}</span>}
        </div>
      </div>
      {onRetry && (
        <button type="button" onClick={onRetry} className="btn-alert-action">
          {t('error.retry')}
        </button>
      )}
    </div>
  );
};
