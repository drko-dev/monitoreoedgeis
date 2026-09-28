import React from 'react';
import { SystemReport } from '../types/installer';
import { useI18n } from '../i18n/I18nContext';

interface SystemCardProps {
  report: SystemReport;
}

export const SystemCard: React.FC<SystemCardProps> = ({ report }) => {
  const { t } = useI18n();
  return (
    <section className="card" aria-labelledby="system-heading">
      <h2 id="system-heading" className="card-title">{t('system.title')}</h2>
      <ul className="check-list" role="list">
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">{t('system.os')}</span>
          <strong className="check-value">{report.os} ({report.arch})</strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">{t('system.edgeVersion')}</span>
          <strong className="check-value">{report.edge_version || 'v1.0.0-dev'} ({report.commit || 'local'})</strong>
        </li>
        <li className="check-item">
          <span className={`check-icon ${report.platform_supported ? 'pass' : 'fail'}`} aria-hidden="true">
            {report.platform_supported ? '✓' : '✗'}
          </span>
          <span className="check-label">{t('system.platform')}</span>
          <strong className="check-value">
            {report.platform_supported ? t('system.supported') : t('system.unsupported')}
          </strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">{t('system.memory')}</span>
          <strong className="check-value">
            {report.free_ram_mb > 0
              ? t('system.memoryValue', { free: report.free_ram_mb, total: report.total_ram_mb })
              : t('system.notMeasured')}
          </strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">{t('system.disk')}</span>
          <strong className="check-value">{t('system.diskValue', { gb: report.free_disk_gb })}</strong>
        </li>
      </ul>
    </section>
  );
};
