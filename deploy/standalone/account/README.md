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

## Email change

When a user signs in with a verified email that differs from the stored one
(same provider issuer and subject), the cabinet takes it over for the user
and all memberships in one transaction, writes `admin.user.email_changed` to
the audit log and sends the old address an `email_changed` notice (through
the notification worker, with the new address masked).

## Data export and deletion

Under Security, users download their data (JSON) and delete their account.
Deletion needs an MFA session and the user's email typed as confirmation; it
ends every session and access, deletes notification data and pseudonymises
the user, memberships and sessions. The audit log and financial records
stay. Merchants the user alone owns are closed (status `disabled`) only with
consent. Platform operators (global bindings) are offboarded by another
operator. The account at the identity provider is not deleted; tell users
to delete it there if they wish.
