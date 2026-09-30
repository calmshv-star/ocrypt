# Workflow release plan

Initial revision: `2540d1c03194af5505f170473662605c05b0367b`.

- [x] User authorization and scope recorded.
- [x] Server and GitHub baseline inspected without secrets.
- [x] Existing CI failures inspected: backend formatting; Jackson CVE-2026-68497.
- [ ] Narrow fixes validated; mandatory CI succeeds.
- [ ] Integrated artifacts independently reviewed.
- [ ] PR merged; exact merge revision tagged and GitHub release published.
- [ ] Server source installed, hashes and isolated tests checked, pointer activated.
- [ ] Runtime preservation and final release references verified.

The orchestrator owns small serial formatting/dependency fixes and deployment.
An independent reviewer owns read-only review of integrated release materials.
Existing run `36736374959` failed backend formatting and supply-chain; all other
functional/image jobs succeeded. Publishing depends on a green replacement run.
Evidence is retained locally under the release work directory and attached to
the published release. Never store credentials or customer records.

Next: validate formatting, patch the Java dependency, review and rerun CI.
