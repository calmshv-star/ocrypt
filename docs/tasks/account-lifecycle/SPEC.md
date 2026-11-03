# account-lifecycle specification

## Requested outcome

REG-5: access recovery, email change and account deletion with data export for cabinet users

## Interview and scope

Source: TZ REG-5 ("Восстановление доступа, смена email, удаление аккаунта с
выгрузкой данных; защита от перебора и одноразовых почтовых доменов"). The
owner asked for no questions; agent decisions are in DECISIONS.md.

Facts found in the code:

- The identity provider (behind Dex) owns passwords, second factors, email
  verification and lockout; the cabinet only sees verified OIDC claims.
- Login refused any session whose verified email differed from the stored
  one, so an email changed at the provider locked the user out.
- Membership access matches the member email to the user email.
- Disposable email domains are already refused at signup (stage 2).
- A last-owner guard forbids revoking a merchant's last active owner.

Behaviour:

1. Recovery: `ADMIN_IDP_RECOVERY_URL` and `ADMIN_IDP_ACCOUNT_URL` (https)
   are served publicly (`GET /admin/v1/auth/account-links`); the sign-in
   screen links to recovery, the Security page to the provider account.
2. Email change: a login with a different verified email for the same
   issuer and subject succeeds; the user and member emails change in one
   transaction, it is audited, and the old address gets a notice with the
   new one masked. A failed sync keeps the old emails consistent and retries
   at the next login.
3. Export: `GET /admin/v1/account/export` downloads a JSON document with the
   profile, memberships, role bindings, the latest 500 sessions,
   notification settings and the latest 1000 audit events by the user.
4. Deletion: `POST /admin/v1/account/delete` (MFA session, own email typed)
   ends all sessions and access, deletes notification data, pseudonymises
   the user, memberships and sessions, frees the provider identity, and
   audits it. Merchants the user alone owns are closed only with consent
   (409 `owner_of_merchants` otherwise) and keep a pseudonymised owner
   record. Platform operators are refused (409 `platform_operator`). The
   audit log and financial records stay.

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Email change taken over everywhere, audited, old address told; owner keeps access | live `S8_account_lifecycle` |
| Unchanged email not rewritten; failed sync keeps the old email | live `S8`, `TestEmailChangedAtTheProviderIsTakenOver` |
| Export holds profile, memberships and audit events | live `S8`, `TestAccountExportIsADownload` |
| Deletion needs own email; sole-owned merchants need consent | `TestAccountDeletionNeedsTheEmailAndConsentToCloseMerchants`, live `S8` |
| Co-owned merchant stays open; membership ends; identity freed | live `S8` |
| Closed merchants keep a pseudonymised owner record | live `S8` |
| Platform operators refused | `TestPlatformOperatorsCannotDeleteThemselves`, live `S8` |
| Account links are https without credentials | `TestAccountLinksAreThePublicProviderPages` |
| Cabinet: provider link, download, deletion flow, refusals, recovery link | `apps/admin/src/Security.test.tsx` |
| Email-change notice in six languages | `TestRenderNamesTheEventInTheUsersLanguage` |

## Out of scope

- Password reset, second-factor reset and brute-force lockout: the identity
  provider's (documented in the runbook).
- Deleting the account at the identity provider.
- Re-opening a closed merchant (an operator task).
