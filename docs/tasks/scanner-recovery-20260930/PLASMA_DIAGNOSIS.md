# Plasma runtime diagnosis and repair prerequisite

Observed 2026-09-30 on server87.120.126.125. Cursor33829865 is retained;
scanner readiness503 and admission error healthy provider quorum unavailable.
The worker83c866d already contains the finalized margin/real token contract
probe code; upgrading this worker is not an evidence-supported repair.

## Read-only actual provider checks

- rpc.plasma.to: chain9745, finalized header, historical cursor block and bounded
  USDT Transfer-log query succeed.
- 9745.rpc.thirdweb.com: chain9745 and some finalized headers succeed, but
  bounded eth_getLogs fails JSON-RPC -32603 (including latest block).
- plasma.drpc.org: HTTP400/code35, chain unavailable on free plan, upgrade
  required. No paid subscription or expense was authorized/created.
- rpc.swiftnodes.io/rpc/plasma: chain9745, finalized header and historical
  bounded USDT Transfer-log query succeed anonymously. This is a candidate,
  not a production-admitted independently verified failure domain. Its upstream
  independence and production access/limits must be established.

Official endpoint references:
https://thirdweb.com/plasma-9745
https://blog.drpc.org/drpc-plasma-rpc-launch/
https://swiftnodes.io/docs/supported-chains
https://swiftnodes.io/docs/method-support

## Concrete required change

Admit a healthy independently operated second provider using a new versioned
RPC snapshot and five normal operation policies, retaining quorum2, finalized
heads, chain/genesis validation, historical log reads, scanner cursor/history,
USDT scanning and all financial parameters. Update scanner runtime selection
only after actual health/admission and independent domain evidence succeed.
Keep existing thirdweb snapshot/runtime selection for rollback. Repeated cursor
advancement and decreasing actual head lag must establish recovery afterward.

The existing application requires an authenticated authorized requester and a
separate approving actor, each with fresh step-up for controlled operations:
platformadmin/service.go require/RequestApproval/Decide/Activate; migration005
CHECK approved_by IS NULL OR approved_by<>requested_by. No authenticated
administrative sessions or independent-provider credentials were found in the
available task context. Root asked the user for existing access while completing
independent financial fixes. Do not invent approving users, synthesize sessions,
write config heads directly, reset health circuits, reduce quorum, proxy both
provider slots to one upstream, or claim this prerequisite is satisfied.
