import { describe, expect, it } from "vitest";
import { isLocale, messages, supportedLocales, translate } from "./locales";
import { en } from "./messages";

describe("locale catalog", () => {
  it("ships all required locales with every English key", () => {
    expect(supportedLocales).toEqual(["en", "zh-CN", "es", "fr", "de", "ru"]);
    const englishKeys = Object.keys(en).sort();
    for (const locale of supportedLocales) {
      expect(Object.keys(messages[locale]).sort()).toEqual(englishKeys);
      expect(Object.values(messages[locale]).every((value) => value.trim().length > 0)).toBe(true);
    }
  });

  it("recognizes only supported locale identifiers", () => {
    expect(isLocale("zh-CN")).toBe(true);
    expect(isLocale("pt-BR")).toBe(false);
  });

  it("translates known strings", () => {
    expect(translate("ru", "page.unmatched.title")).toBe("Платежи на проверке");
    expect(translate("zh-CN", "landing.hero.primary")).toBe("体验沙盒");
  });
});
