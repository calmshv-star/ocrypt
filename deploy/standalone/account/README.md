# Cabinet accounts: recovery, email change, deletion (REG-5)

## Identity provider responsibilities

Ocrypt never stores passwords or second factors. The identity provider
behind Dex must offer, and the operator must enable:

- password reset and second-factor reset (with identity checks);
- lockout or throttling after failed sign-in attempts (brute force);
- email change with verification;
- refusal of disposable email domains at registration (the cabinet also
  refuses them at signup).

Set on the admin BFF:

| Variable | Meaning |
| --- | --- |
| `ADMIN_IDP_RECOVERY_URL` | https page for a forgotten password or lost second factor; shown on the sign-in screen |
| `ADMIN_IDP_ACCOUNT_URL` | https page where users manage password, second factor and email; shown under Security |
| `ADMIN_FOLLOW_IDP_EMAIL` | `true` (default): take over a verified email changed at the provider. Set `false` for a provider that may reuse subjects (some LDAP or SAML connectors); a changed email then refuses the login, as before |

## Email change

When a user signs in (or accepts an invitation, or returns through signup)
with a verified email that differs from the stored one (same provider issuer
and subject), the cabinet takes it over for the user and all memberships in
one transaction, writes `admin.user.email_changed` to the audit log and
sends the old address an `email_changed` notice with the new address masked
(and the linked Telegram chat, at most three a day). A disposable or
sanctions-listed domain is not taken over (`admin.user.email_change_refused`);
the user keeps the stored email.

## Data export and deletion

Under Security, users download their data (JSON) and delete their account.
Deletion needs a sign-in or step-up within the step-up window (an old open
session is sent to step up first) and the user's email typed as
confirmation. It ends every session and access, deletes notification data,
pseudonymises the user, memberships, sessions and invitation enrolments, and
emails the account's address. The audit log, financial records and the
invitation records (immutable) stay. Merchants the user alone owns (counting
only co-owners who can still sign in) are closed (status `disabled`) only
when the user named exactly those merchants. Platform operators are
offboarded by another operator; a signup under screening review is decided
first. The account at the identity provider is not deleted; tell users to
delete it there if they wish.
