// Single source of truth for every documentation link the Installer shows.
// Real routes only -- each slug below was verified against the live SaaS
// Help Center (`geocam/routers/help.py`, HELP_DOC_ARTICLES) before being
// added here. No component should build a docs URL by hand.

export const SAAS_PUBLIC_BASE_URL = 'https://vps-6387636-x.dattaweb.com';
const HELP_ARTICLE_PATH = '/ayuda/articulos';

function articleUrl(slug: string): string {
  return `${SAAS_PUBLIC_BASE_URL}${HELP_ARTICLE_PATH}/${slug}`;
}

export const docsLinks = {
  installation: articleUrl('manual-instalar-edge-app'),
  enrollment: articleUrl('manual-vincular-edge'),
  deviceRole: articleUrl('manual-rol-dispositivo'),
  processingModes: articleUrl('manual-modos-procesamiento-instalador'),
  cameraDiscovery: articleUrl('manual-camara-ip-instalador'),
  cameraCredentials: articleUrl('manual-camara-ip-instalador'),
  dvrNvr: articleUrl('manual-dvr-nvr-instalador'),
  commissioning: articleUrl('manual-diagnostico-edge'),
  troubleshooting: articleUrl('manual-solucion-problemas-instalador'),
} as const;

export type DocsLinkKey = keyof typeof docsLinks;
