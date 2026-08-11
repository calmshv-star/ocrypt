# Contributing

By intentionally submitting a contribution for inclusion, you agree that it
is licensed under the repository's Apache License 2.0 as described in section
5 of that license, unless the submission is conspicuously marked "Not a
Contribution." You represent that you have the right to submit the work and
that you have disclosed any third-party material and its license.

## Language

Source code, identifiers, comments, commit messages, logs, and API contracts use English. User-facing strings must come from the shared localization catalog. Do not hard-code UI copy in components.

## Boundaries

- Domain packages do not import HTTP, database, broker, RPC SDK, or frontend types.
- Chain adapters persist observations before matching.
- Handlers do not write ledger rows directly.
- AI output is advisory and cannot call settlement or treasury commands.
- Customer fulfillment logic does not belong in this repository.

## Quality gates

Every change must include proportionate unit or contract tests, pass formatting, lint, type checking, and relevant end-to-end scenarios. Financial and security-sensitive changes also require invariant, concurrency, replay, and negative tests.

## Localization

English is the canonical message catalog. Every key must exist in `zh-CN`, `es`, `fr`, `de`, and `ru`. CI rejects missing keys, accidental empty values, and unlocalized user-facing text.

## Third-party code

Before copying a component or source fragment, record its repository, exact revision, original path, copyright, and license in `THIRD_PARTY_NOTICES.md`. Do not copy premium examples or assets whose license differs from the source repository.

## Sign-off

Commits should include a `Signed-off-by: Name <email>` trailer. The sign-off
certifies that the contributor created the contribution or has the right to
submit it under Apache-2.0 and understands that the contribution and public
metadata will be maintained indefinitely as part of the project history.

## Conduct

Participation must be professional and respectful. Harassment, threats,
discrimination, deliberate exposure of personal information, and abuse of the
security-reporting process are not accepted. Maintainers may moderate or
remove participation that creates a safety or legal risk for the project or
its community.
