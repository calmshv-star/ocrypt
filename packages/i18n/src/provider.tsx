import {
  createContext,
  type PropsWithChildren,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState
} from "react";
import {
  isLocale,
  localeNames,
  type Locale,
  supportedLocales,
  translate
} from "./locales";
import type { MessageKey } from "./messages";

const STORAGE_KEY = "merchant-platform-locale";

type I18nContextValue = {
  locale: Locale;
  locales: readonly Locale[];
  localeNames: Record<Locale, string>;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, values?: Record<string, string | number>) => string;
  formatAmount: (value: number, currency?: string) => string;
  formatDate: (value: Date | string | number, options?: Intl.DateTimeFormatOptions) => string;
};

const I18nContext = createContext<I18nContextValue | null>(null);

function detectLocale(): Locale {
  if (typeof window !== "undefined") {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (isLocale(saved)) return saved;
    const browserLocale = window.navigator.language;
    if (isLocale(browserLocale)) return browserLocale;
    const language = browserLocale.split("-")[0];
    const match = supportedLocales.find((locale) => locale.split("-")[0] === language);
    if (match) return match;
  }
  return "en";
}

export function I18nProvider({ children, initialLocale }: PropsWithChildren<{ initialLocale?: Locale }>) {
  const [locale, setLocaleState] = useState<Locale>(initialLocale ?? detectLocale);

  const setLocale = useCallback((nextLocale: Locale) => {
    setLocaleState(nextLocale);
    if (typeof window !== "undefined") window.localStorage.setItem(STORAGE_KEY, nextLocale);
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const value = useMemo<I18nContextValue>(() => ({
    locale,
    locales: supportedLocales,
    localeNames,
    setLocale,
    t: (key, values) => translate(locale, key, values),
    formatAmount: (amount, currency = "USD") =>
      new Intl.NumberFormat(locale, {
        style: "currency",
        currency,
        maximumFractionDigits: 2
      }).format(amount),
    formatDate: (date, options) =>
      new Intl.DateTimeFormat(locale, options ?? {
        dateStyle: "medium",
        timeStyle: "short"
      }).format(new Date(date))
  }), [locale, setLocale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const context = useContext(I18nContext);
  if (!context) throw new Error("useI18n must be used inside I18nProvider");
  return context;
}
