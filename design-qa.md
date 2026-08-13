# Checkout payment guidance — design QA

## Evidence

- Source visual truth: `/var/folders/mr/5d6wtj0d6fv284lyr9pxwbgc0000gn/T/TemporaryItems/NSIRD_screencaptureui_6tNqVH/Снимок экрана — 2026-08-13 в 10.26.37.png`
- Browser-rendered implementation, desktop light: `/private/tmp/ocrypt-checkout-final-desktop-light.png`
- Browser-rendered implementation, desktop dark: `/private/tmp/ocrypt-checkout-final-desktop-dark.png`
- Browser-rendered implementation, mobile light: `/private/tmp/ocrypt-checkout-final-mobile-light.png`
- Full-view comparison: `/private/tmp/ocrypt-checkout-reference-comparison.png`
- Focused payment-region comparison after the final fix: `/private/tmp/ocrypt-checkout-payment-focused-final.png`
- Source pixels: 1368 × 1724 PNG at 144 dpi.
- Desktop implementation: 1280 × 1125 JPEG at 72 dpi; CSS viewport 1280 × 720.
- Mobile implementation: 390 × 1816 JPEG at 72 dpi; CSS viewport 390 × 844.
- Dark implementation: 1280 × 1125 JPEG at 72 dpi; CSS viewport 1280 × 720.
- Density normalization: the source was resized proportionally to 1280 × 1613 for the full-view comparison; both panels therefore use the same pixel width. The focused comparison uses two 734 px-wide payment regions.
- State: Russian locale, pending Tron/USDT on-chain route, active countdown, usable QR/address/amount controls.

## Findings

- No actionable P0, P1, or P2 findings remain.
- Typography: the implementation keeps Ocrypt's established sans/monospace hierarchy. Amount, asset, address, network, fiat equivalent, and explanatory text remain visually distinct without importing the reference brand font.
- Spacing and layout: desktop keeps the existing payment/details grid; mobile collapses to one column with no horizontal overflow. The added guidance follows the existing 10–14 px radius and compact card rhythm.
- Colors and tokens: the information is rendered with Ocrypt's neutral and amber semantic tokens in both light and dark themes. The reference's blue fee treatment was intentionally not copied because Ocrypt's design direction excludes blue UI.
- Image and icon quality: the QR remains a functional generated canvas. No Heleket logo, provider artwork, or foreign brand assets were copied. New icons come from the project's existing icon family.
- Copy and content: the page now exposes the fiat equivalent, address-only QR behavior, separate network/exchange fee handling, actual recipient-amount requirement, automatic detection behavior, and the remaining payment time. The existing copy actions remain explicit.
- Accessibility and behavior: amount copy changes to the localized copied state; theme switching works; the Russian locale is rendered; browser console contains no warnings or errors; mobile document width equals the 390 px viewport.

## Comparison history

1. Initial comparison found a P2 density issue: the existing pending-status message and a new standalone after-payment warning repeated the same instruction in adjacent blocks.
2. The standalone block was removed and its useful content was merged into the live pending-status message.
3. The page was rebuilt, reloaded, and captured again. The focused final comparison shows one fee warning and one live after-payment/status message, with no duplicate instruction.

## Primary interactions tested

- Copy exact amount and observe the localized `Скопировано` state.
- Switch between light and dark themes.
- Render the Russian locale.
- Resize from 1280 px desktop to 390 px mobile and verify zero horizontal overflow.
- Confirm that QR, exact amount, address, fiat equivalent, fee warning, countdown, receipt assistance, and live status remain visible.

## Follow-up polish

- No P3 work is required for this scope.

final result: passed
