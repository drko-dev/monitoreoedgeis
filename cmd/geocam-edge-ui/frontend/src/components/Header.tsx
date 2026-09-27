import React from 'react';

interface HeaderProps {
  version: string;
}

export const Header: React.FC<HeaderProps> = ({ version }) => {
  return (
    <header className="app-header">
      <div className="brand-group">
        <div className="logo-badge" aria-hidden="true">
          <span className="logo-icon">👁</span>
        </div>
        <div>
          <h1 className="product-title">GEO CAM Edge</h1>
          <p className="product-subtitle">Installer & Appliance Setup</p>
        </div>
      </div>
      <div className="version-pill" aria-label={`Edge Version ${version}`}>
        <span>Version {version || 'v1.0.0'}</span>
      </div>
    </header>
  );
};
