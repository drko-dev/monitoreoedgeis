import React, { createContext, useCallback, useContext, useMemo, useState } from 'react';
import { Language, TranslationKey, dictionaries } from './translations';
import { interpolate } from './interpolate';

const STORAGE_KEY = 'geocam-edge-language';
const DEFAULT_LANGUAGE: Language = 'es';

function isLanguage(value: string | null): value is Language {
  return value === 'es' || value === 'en';
}

function readStoredLanguage(): Language {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    return isLanguage(stored) ? stored : DEFAULT_LANGUAGE;
  } catch {
    // localStorage can throw (private mode, disabled storage) -- fall back
    // to the default rather than crash the installer over a preference.
    return DEFAULT_LANGUAGE;
  }
}

interface I18nContextValue {
  language: Language;
  setLanguage: (lang: Language) => void;
  t: (key: TranslationKey, vars?: Record<string, string | number>) => string;
}

const I18nContext = createContext<I18nContextValue | null>(null);

export const I18nProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [language, setLanguageState] = useState<Language>(readStoredLanguage);

  const setLanguage = useCallback((lang: Language) => {
    setLanguageState(lang);
    try {
      window.localStorage.setItem(STORAGE_KEY, lang);
    } catch {
      // Non-persistent preference for this session is an acceptable
      // degradation -- never block the language switch on storage failing.
    }
  }, []);

  const t = useCallback(
    (key: TranslationKey, vars?: Record<string, string | number>): string => {
      return interpolate(dictionaries[language][key], vars);
    },
    [language],
  );

  const value = useMemo(() => ({ language, setLanguage, t }), [language, setLanguage, t]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
};

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) {
    throw new Error('useI18n must be used within an I18nProvider');
  }
  return ctx;
}
