import { de } from "./de";
import { es } from "./es";
import { fr } from "./fr";
import { en, type MessageKey, type Messages } from "./messages";
import { ru } from "./ru";
import { zhCN } from "./zh";

export type Locale = "en" | "zh-CN" | "es" | "fr" | "de" | "ru";

export const localeNames: Record<Locale, string> = {
  en: "English",
  "zh-CN": "简体中文",
  es: "Español",
  fr: "Français",
  de: "Deutsch",
  ru: "Русский"
};

export const supportedLocales = Object.keys(localeNames) as Locale[];

export const messages: Record<Locale, Messages> = {
  en,
  "zh-CN": zhCN,
  es,
  fr,
  de,
  ru
};

export function isLocale(value: string | null | undefined): value is Locale {
  return supportedLocales.includes(value as Locale);
}

export function translate(locale: Locale, key: MessageKey, values?: Record<string, string | number>): string {
  const template = messages[locale][key];
  if (!values) return template;
  return template.replace(/\{(\w+)\}/g, (match, token: string) =>
    values[token] === undefined ? match : String(values[token])
  );
}
