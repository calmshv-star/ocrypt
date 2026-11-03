import type { MessageKey } from "@merchant/i18n";

export type SignInMethod = "passkey" | "otp" | "sms" | "pwd" | "mfa" | "other";

const METHODS: Record<string, SignInMethod> = { hwk: "passkey", swk: "passkey", fido: "passkey", webauthn: "passkey", passkey: "passkey", otp: "otp", sms: "sms", tel: "sms", pwd: "pwd", mfa: "mfa" };

// signInMethods turns the OIDC amr values (RFC 8176 and the common passkey
// names) into the few methods the cabinet can name, once each.
export function signInMethods(amr: string[]): SignInMethod[] {
  return [...new Set(amr.map((value) => METHODS[value.toLowerCase()] ?? "other"))];
}

// deviceName reads the "Browser/System" label the server stored at sign-in;
// anything else is an unknown device.
export function deviceName(label: string, t: (key: MessageKey) => string): string {
  const match = /^([A-Za-z]{1,16})\/([A-Za-z]{0,16})$/.exec(label);
  if (!match) return t("security.device.unknown");
  const browser = match[1] === "Other" ? t("security.device.otherBrowser") : match[1]!;
  return match[2] ? `${browser} · ${match[2]}` : browser;
}
