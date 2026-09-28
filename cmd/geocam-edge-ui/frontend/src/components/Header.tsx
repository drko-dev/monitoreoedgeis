import React, { useState } from 'react';
import { useI18n } from '../i18n/I18nContext';
import { Language } from '../i18n/translations';
import logoMark from '../assets/geocam-isotype.png';
import logoFull from '../assets/geocam-logo-full.png';

interface HeaderProps {
  version: string;
}

export const Header: React.FC<HeaderProps> = ({ version }) => {
  const { language, setLanguage, t } = useI18n();
  const [showAbout, setShowAbout] = useState(false);

  return (
    <header className="app-header">
      <div className="brand-group">
        <button
          type="button"
          className="logo-badge-button"
          onClick={() => setShowAbout((v) => !v)}
          aria-expanded={showAbout}
          aria-controls="about-panel"
          aria-label="GEO CAM Edge"
        >
          <img className="logo-badge" src={logoMark} alt="" />
        </button>
        <div>
          <h1 className="product-title">GEO CAM Edge</h1>
          <p className="product-subtitle">{t('header.subtitle')}</p>
        </div>
      </div>
      <div className="header-actions">
        <div
          className="language-switcher"
          role="group"
          aria-label={t('header.languageLabel')}
        >
          {(['es', 'en'] as Language[]).map((lang) => (
            <button
              key={lang}
              type="button"
              className={`lang-option ${language === lang ? 'lang-option-active' : ''}`}
              aria-pressed={language === lang}
              onClick={() => setLanguage(lang)}
            >
              {lang.toUpperCase()}
            </button>
          ))}
        </div>
        <div className="version-pill" aria-label={`Edge Version ${version}`}>
          <span>{t('header.version', { version: version || 'v1.0.0' })}</span>
        </div>
      </div>
      {showAbout && (
        <div id="about-panel" className="about-panel">
          <img className="about-logo" src={logoFull} alt="GEO CAM — Visión Inteligente, para un mundo más seguro" />
          <p className="about-version">GEO CAM Edge Installer · {version || 'v1.0.0'}</p>
        </div>
      )}
    </header>
  );
};
