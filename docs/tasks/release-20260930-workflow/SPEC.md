# Workflow release specification

## Requested outcome

The user authorized publishing the completed ocrypt workflow to GitHub and the
existing server as a new version on 2026-09-30. Version: `v2026.09.30-workflow`.

## Scope

Publish PR #109 to main after mandatory CI passes, tag that exact merge revision,
publish a GitHub release with a source archive and checksum, and install the same
source/tooling on `87.120.126.125` under `/opt/ocrypt/releases/`. Activate the
workflow through `/opt/ocrypt/agent-workflow-current`. This is a development
tooling release; existing payment containers, database, credentials, configuration,
rate interval (30 minutes), provider quorum, and monetary parameters stay as-is.

Release-blocking baseline failures may be fixed narrowly: canonical Go formatting
and Jackson 2.18.8 to the compatible security patch 2.18.10 in the Java SDK.
Do not weaken CI or suppress the vulnerability. No production transactions.

## Acceptance matrix

| ID | Requirement | Check | Evidence |
| --- | --- | --- | --- |
| R1 | Integrated release passes mandatory CI | GitHub release-gate success for PR head | Recorded Actions run |
| R2 | GitHub main, tag and release identify one revision | REST API and local Git comparison | Release metadata |
| R3 | Server contains that exact source bundle | Archive and extracted file SHA-256 verification | Server manifest |
| R4 | Workflow runs on server | 13 workflow unit tests in isolated Python 3.13 container, network disabled | Server validation log |
| R5 | Tooling install preserves runtime | Read-only container IDs before/after; checkout HTTP response | Baseline and final probes |
| R6 | Baseline release blockers fixed proportionately | gofmt clean; Java tests and security scan green | CI and local validation |

## Prerequisites and limits

GitHub Git credential and existing SSH key authorize the requested publishing.
The host Python 3.10 cannot execute role tests requiring tomllib; use its existing
Python 3.13 Docker image. No live financial tests use production as a fixture.
Some existing scanners are unhealthy and the core proxy has historical restarts;
record these baseline conditions without claiming this tooling release resolves them.
