import React from 'react';
import { InstallerState } from '../types/installer';

interface StateCardProps {
  state: InstallerState;
}

export const StateCard: React.FC<StateCardProps> = ({ state }) => {
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
    switch (state.state) {
      case 'NEEDS_ENROLLMENT':
        return 'Needs Enrollment';
      case 'NEW':
        return 'Fresh Installation';
      case 'ENROLLED':
        return 'Enrolled & Verified';
      case 'ACTION_REQUIRED':
        return 'Action Required';
      case 'BLOCKED':
        return 'Installation Blocked';
      default:
        return state.state;
    }
  };

  return (
    <section className="card" aria-labelledby="state-heading">
      <div className="card-header-row">
        <h2 id="state-heading" className="card-title">Current State</h2>
        <span className={`badge ${getBadgeClass()}`}>{getReadableStateName()}</span>
      </div>
      <p className="state-message">{state.safe_message}</p>
      {state.device_id && (
        <div className="device-id-box">
          <span className="device-id-label">Device Identifier:</span>
          <code className="device-id-code">{state.device_id}</code>
        </div>
      )}
    </section>
  );
};
