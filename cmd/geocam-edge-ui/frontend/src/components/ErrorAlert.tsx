import React from 'react';

interface ErrorAlertProps {
  message: string;
  code?: string;
  onRetry?: () => void;
}

export const ErrorAlert: React.FC<ErrorAlertProps> = ({ message, code, onRetry }) => {
  return (
    <div className="alert alert-danger" role="alert">
      <div className="alert-content">
        <span className="alert-icon" aria-hidden="true">⚠️</span>
        <div>
          <h4 className="alert-title">Attention Required</h4>
          <p className="alert-message">{message}</p>
          {code && <span className="alert-code">Code: {code}</span>}
        </div>
      </div>
      {onRetry && (
        <button type="button" onClick={onRetry} className="btn-alert-action">
          Retry
        </button>
      )}
    </div>
  );
};
