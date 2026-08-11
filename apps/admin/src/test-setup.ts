import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: () => ({ matches: false, addEventListener: () => undefined, removeEventListener: () => undefined })
});

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});
