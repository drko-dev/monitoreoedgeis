import React from 'react';
import { SystemReport } from '../types/installer';

interface SystemCardProps {
  report: SystemReport;
}

export const SystemCard: React.FC<SystemCardProps> = ({ report }) => {
  return (
    <section className="card" aria-labelledby="system-heading">
      <h2 id="system-heading" className="card-title">System Environment</h2>
      <ul className="check-list" role="list">
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">Operating System:</span>
          <strong className="check-value">{report.os} ({report.arch})</strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">Edge Agent Version:</span>
          <strong className="check-value">{report.edge_version || 'v1.0.0-dev'} ({report.commit || 'local'})</strong>
        </li>
        <li className="check-item">
          <span className={`check-icon ${report.platform_supported ? 'pass' : 'fail'}`} aria-hidden="true">
            {report.platform_supported ? '✓' : '✗'}
          </span>
          <span className="check-label">Platform Architecture:</span>
          <strong className="check-value">
            {report.platform_supported ? 'Supported' : 'Unsupported Platform'}
          </strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">Available Memory:</span>
          <strong className="check-value">
            {report.free_ram_mb > 0 ? `${report.free_ram_mb} MB free / ${report.total_ram_mb} MB total` : 'Not Measured'}
          </strong>
        </li>
        <li className="check-item">
          <span className="check-icon pass" aria-hidden="true">✓</span>
          <span className="check-label">Available Disk Space:</span>
          <strong className="check-value">{report.free_disk_gb} GB Available</strong>
        </li>
      </ul>
    </section>
  );
};
