# Third-Party Notices

This product incorporates or adapts permissively licensed open-source software. This file must be shipped with any source or on-prem distribution and regenerated from the final dependency graph before release.

## shadcn-admin-kit

- Project: https://github.com/marmelab/shadcn-admin-kit
- Revision reviewed: `97843b9363f08fa649b43f25cb36680e5904bccc`
- Upstream license at that revision: https://github.com/marmelab/shadcn-admin-kit/blob/97843b9363f08fa649b43f25cb36680e5904bccc/LICENSE
- Copyright: 2025 marmelab
- License: MIT
- Local use and provenance:
  - `packages/ui/src/app-shell.tsx` adapts the structural and interaction patterns from `src/components/admin/app-sidebar.tsx`, `src/components/admin/layout.tsx`, `src/components/admin/theme-provider.tsx`, and `src/components/admin/theme-mode-toggle.tsx`.
  - `packages/ui/src/data-table.tsx` and `packages/ui/src/page.tsx` adapt table, toolbar, list-header, and responsive administration patterns from `src/components/admin/data-table.tsx` and related list components.
  - The implementation was rewritten for the local neutral design system. Upstream names, logos, demo data, and application-specific authentication were not copied.

MIT License

Copyright (c) 2025 marmelab

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## shadcn-admin

- Project: https://github.com/satnaing/shadcn-admin
- Revision reviewed: `e16c87f213a5ba5e45964e9b67c792105ec74d26`
- Upstream license at that revision: https://github.com/satnaing/shadcn-admin/blob/e16c87f213a5ba5e45964e9b67c792105ec74d26/LICENSE
- Copyright: 2024 Sat Naing
- License: MIT
- Local use and provenance:
  - `packages/ui/src/app-shell.tsx` adapts responsive sidebar, header, mobile drawer, skip-link, command-menu, and theme patterns from `src/components/layout/app-sidebar.tsx`, `src/components/layout/header.tsx`, `src/components/layout/main.tsx`, `src/components/command-menu.tsx`, `src/components/skip-to-main.tsx`, and `src/context/theme-provider.tsx`.
  - `packages/ui/src/data-table.tsx` and `packages/ui/src/page.tsx` adapt selected data-table toolbar, pagination, column-header, and content-layout patterns.
  - No Clerk integration, demo authentication, upstream branding, logos, or sample business data is included.

MIT License

Copyright (c) 2024 Sat Naing

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## shadcn/ui

- Project: https://github.com/shadcn-ui/ui
- Revision reviewed: `d14b6e69a91f0fc99e31a7adb26a48d661df9911`
- Upstream license at that revision: https://github.com/shadcn-ui/ui/blob/d14b6e69a91f0fc99e31a7adb26a48d661df9911/LICENSE.md
- Copyright: 2023 shadcn
- License: MIT
- Local use and provenance:
  - `packages/ui/src/primitives.tsx` adapts the composable button, card, input, select, badge, avatar, visually-hidden, and skeleton conventions from the shadcn/ui registry components.
  - `packages/ui/src/theme.tsx` and `packages/ui/src/styles.css` adapt the theme-token and variant composition approach while using project-owned class names and styling.
  - The local components are rewritten React wrappers; no upstream generated project, branding, examples, or registry metadata is shipped.

MIT License

Copyright (c) 2023 shadcn

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## node-qrcode

- Project: https://github.com/soldair/node-qrcode
- Package version: `qrcode@1.5.4`
- License distributed with the package: `node_modules/qrcode/LICENSE`
- Copyright: 2012 Ryan Day
- License: MIT
- Local use and provenance:
  - `apps/checkout/src/App.tsx` uses the browser canvas API to render the exact verified payment route as a QR code.
  - No upstream application UI, examples, branding, or sample data is copied.

The MIT License (MIT)

Copyright (c) 2012 Ryan Day

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## NATS Go client

- Project: https://github.com/nats-io/nats.go
- Exact module version: `github.com/nats-io/nats.go v1.52.0`
- Release provenance: https://github.com/nats-io/nats.go/releases/tag/v1.52.0
- Upstream license at that version: https://github.com/nats-io/nats.go/blob/v1.52.0/LICENSE
- Copyright: 2012-2026 The NATS Authors
- License: Apache License 2.0
- Local use and provenance:
  - `backend/internal/eventbus` uses the maintained official Go client for
    TLS-authenticated JetStream publish acknowledgements and durable pulls.
  - Integrity hashes are pinned in `backend/go.sum`; release SBOM generation
records this module and its transitive dependency graph.

This distribution uses the work under the Apache License, Version 2.0. A copy
is available at https://www.apache.org/licenses/LICENSE-2.0. Unless required by
law or agreed in writing, it is provided without warranties or conditions.

## Dex web templates

- Project: https://github.com/dexidp/dex
- Exact upstream version: `v2.45.1`
- Upstream license: https://github.com/dexidp/dex/blob/v2.45.1/LICENSE
- Copyright: The Dex Authors
- License: Apache License 2.0
- Local use and provenance:
  - `deploy/standalone/dex-web` vendors the upstream template and static-asset layout required by Dex's supported `frontend.dir` configuration.
  - `templates/header.html`, `templates/footer.html`, and `templates/password.html` are adapted for the Ocrypt administrative sign-in experience; `themes/ocrypt` is project-owned styling and artwork.
  - Connector icons and the remaining templates are unmodified upstream support assets.

This distribution uses the work under the Apache License, Version 2.0. A copy
is available in the repository root `LICENSE` file and at
https://www.apache.org/licenses/LICENSE-2.0. The work is provided without
warranties or conditions unless required by applicable law.

## Release requirement

The installed JavaScript dependency graph reviewed for this publication contained
408 package manifests: MIT, ISC, Apache-2.0, 0BSD, BSD-2-Clause,
BSD-3-Clause, MPL-2.0, and CC-BY-4.0. No package had an undeclared license,
and no GPL, AGPL, LGPL, SSPL, or BSL identifier was present.

Two transitive components require particular care:

- `axe-core@4.12.1` is MPL-2.0. MPL-2.0 is file-level copyleft: if an MPL-covered
  file itself is modified and distributed, the corresponding source for that
  file and its license notices must remain available under MPL-2.0. Merely
  using the unmodified testing dependency does not relicense this project.
- `caniuse-lite@1.0.30001809` includes data distributed under CC-BY-4.0.
  Redistributions must preserve the upstream attribution and license notice.
  It is a build-tool data dependency and does not relicense project source.

Package versions above describe the reviewed local lockfile/install snapshot.
The final product also depends on packages distributed under their own
licenses. CI must produce a machine-readable software bill of materials and a
reviewed human-readable license inventory from the final lockfiles and built
images. Premium, enterprise, source-available, or copyleft components must not
be introduced without an explicit legal and architectural decision.
