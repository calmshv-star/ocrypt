# Release decisions

- 2026-09-30: User explicitly authorized server and GitHub publication. Choose
  `v2026.09.30-workflow` following the repository date-plus-feature convention.
- Publish source/development tooling because the accepted changes govern how
  ocrypt is developed. No payment-runtime code change warrants rebuilding or
  restarting production services. The server gets a versioned complete source
  archive, verification manifest, and an atomic tooling pointer.
- Fix baseline release blockers without disabling checks: gofmt only and Jackson
  2.18.10, the security patch for CVE-2026-68497. Upstream release notes:
  https://github.com/FasterXML/jackson/wiki/Jackson-Release-2.18.10
- Use GitHub REST with the existing Git credential because the optional CI
  connector is not connected. Keep credentials only in process memory.
- Host Python 3.10 is insufficient; use the installed Python 3.13 container
  with no network for workflow validation.
