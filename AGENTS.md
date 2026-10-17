# CLI owner instructions

This repository owns the Liapoldus CLI, its tests, workflows and documentation.
Edit only this repository for CLI work; inspect sibling owner contracts read-only
and report API differences to their owners. Preserve existing user WIP. Do not
commit or push unless explicitly requested.

Runtime entities are Services. Only Studio editor extensions are StudioPlugins.
The control plane is Studio → CLI → Core → Service SDK REST. Independent
services communicate through Wool on the data plane. CLI imports neither
`github.com/Liapoldus/service-sdk/v2` nor `github.com/Liapoldus/wool/v3` and never
calls service REST. Management resources use `/api/services` and
`/api/service-links`; no old command or HTTP route aliases.

Keep neutral IDs, application methods, immutable generation bytes/digests and
historical reports unchanged. `artifacts.json` already has neutral fields: keep
its path, registrations, digests, local binary paths, optional arguments and trust
decisions. Listing must not rewrite it. Projects already use
`services/<id>/service.yaml`; do not infer binary identity from an old manifest
or silently omit legacy runtime input. SDK identity certificates must use the
canonical service URI and match the Wool replica identity.

Docker stages operator-built binaries and never builds images or binaries.
Use apply_patch for authored edits; mechanical nomenclature rewrites are allowed.
Start with focused regression tests, then run `GOWORK=off make check` and
`GOWORK=off make test-race`. Run the Core contract check with
`LIAPOLDUS_CORE_OPENAPI` pointing to the owner's current contract. CI dependency
revisions must remain pinned to real commits; report unpublished owner migration
as a blocker rather than substituting floating main or fabricated hashes.

## Studio configurator contract

The canonical cross-repository scope is [`../studio/plan.md`](../studio/plan.md).
Expose service manifest/schema/status and settings/resource/restart workflows as
typed CLI commands/results backed only by Core Management API. CLI must never
request service `/manifest`, `/config-schema`, readiness, identity or lifecycle
REST directly; Core uses Service SDK REST. Docker CLI is permitted only for
operator-owned local binary staging/runtime/logs/inspect/allowlisted exec; never
build binaries or images.

Configuration apply must support an immutable, digest-identified working-tree
snapshot; `HEAD` alone is not sufficient for Studio draft auto-apply. Keep target
serialization, cancellation, bounded events, idempotency and explicit
confirmation/recovery in the CLI operation contract. Do not accept arbitrary
renderer-supplied args, shell strings, executable paths or secret values.
