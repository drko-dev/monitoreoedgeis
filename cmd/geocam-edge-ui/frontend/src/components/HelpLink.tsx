import React from 'react';
import { openExternal } from '../utils/openExternal';

interface HelpLinkProps {
  href: string;
  children: React.ReactNode;
}

// Small, secondary "view guide" affordance for contextual help -- never a
// paragraph inline in the wizard. The long explanation lives in the SaaS
// (see docs/runbooks/EDGE_INSTALLER_UI_UX.md).
export const HelpLink: React.FC<HelpLinkProps> = ({ href, children }) => (
  <button
    type="button"
    className="btn-link help-link"
    onClick={() => openExternal(href)}
  >
    <span aria-hidden="true">?</span> {children}
  </button>
);
