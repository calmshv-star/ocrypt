# same-customer-match-20261011 plan

Goal: Settle small overpayments against equivalent repeated same-customer orders exactly once, fulfill the approved Showy payment, and publish the verified repair

Initial source revision: `92a2caf37a8291611996ab1eecb5ba002251ff87`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [ ] Tests authored; expected failures inspected (or artifact validation defined).
- [ ] Dependencies, writer ownership, and common base assigned.
- [ ] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Base is current origin/main `92a2caf37a8291611996ab1eecb5ba002251ff87`; fetched and
verified clean before starting. The previous Solana task/branches are preserved.
Read-only production evidence confirms a score-100/100 overpayment tie with empty,
equal metadata and the same authenticated customer/economics. No match or manual
resolution existed at the initial check. Preserve private identifiers outside Git.

## Resume / next action

First fulfill the explicitly approved payment through the existing verified manual
workflow. Commit this specification, delegate isolated tests-first work, inspect
red evidence, then assign implementation and independent review serially.
