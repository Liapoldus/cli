# CLI — Studio configurator contract and runtime TODO

## Local secret handoff checkpoint — 2026-10-19

- The installed `/Users/docup/go/bin/liapoldus` is the approved Darwin/arm64
  build from commit `d0191b8`, SHA-256
  `1381dd4239795af2b64f9a1878d623eaa0de5c162d2d9895d32f018e010f00dd`; the
  previous binary is backed up under
  `/tmp/liapoldus-local-cli-backup-20261019-admission`. Fresh typed `runtime
  status` reports Core, Echo and Server containers running, and `dev plan` is
  valid at working-tree digest
  `sha256:9c1d1ade4026d0ee87827c518d863f37e0ea829fd4638a7eb0ff964d565bffa8`.
  Core reports Echo/Server at generation 96; Server 1.1.12 is still the running
  trusted artifact.
- The Project contains `/echo` and the temporary
  `/__codex_auth_smoke_20261019` candidate route with one opaque auth reference.
  Runtime has not applied it: `/echo` returns HTTP 200 and the temporary path
  returns 404. Typed `service secret list` shows two active and three revoked
  synthetic references, with no pending registration or rotation. Values
  remain in the local vault and were not read or logged.
- Repeated `dev apply --confirm` for this authorized local candidate returned
  Core HTTP 409 `bundle_apply_failed: service configuration revision conflict`.
  A fresh `service status server` still reports generation 96, the current
  Server digest and no pending generation; this attempt did not create a
  recoverable Core operation. The candidate remains saved in Project and must
  not be reported as active. The newer Core candidate is required to continue
  diagnosis without inspecting Core's private database.
- Latest source is published: SDK `1f68ac6`, Core `aecc186`, Server
  `80ffb23` (1.1.14), and CLI-owned checkpoint follows below. The Linux/arm64
  Core candidate digest is
  `5b8ae79f3ef1098861424ee9d6587d2194b4deeceba6a6ae1d993741fff56391`; Server
  1.1.14 candidate digest is
  `be991dab5b250461963ef46ad713fdbb3b1f2322915376c0ec0519623998a74e`.
  Neither has been installed. Read-only artifact inventory confirms Server
  1.1.14 is not registered yet; the 1.1.12 rollback artifact remains trusted.
  The exact-digest local action gate is still required before registration,
  trust, pin change, or restart.
- Re-ran the CLI owner gates: `make check` with `GOWORK=off GOFLAGS=-p=1` and
  `LIAPOLDUS_CORE_OPENAPI` set to the current Core OpenAPI path, plus
  `make test-race` with `GOWORK=off GOFLAGS=-p=1`; both pass. The remaining
  live candidate validation, authenticated request, rotation ACK and cleanup
  remain after the binaries pass the action gate. Preserve `/echo`; production
  is out of scope.

## Bundle admission blockers and latest local runtime read — superseded — 2026-10-18

- CLI source now reads Core plan `blockedBy` codes and stops before local
  secret staging or writing a recovery intent when an apply is already blocked.
  Regression tests cover this refusal. `GOWORK=off GOFLAGS=-p=1
  LIAPOLDUS_CORE_OPENAPI="/Users/docup/Projects/Liapoldus Engine/core/contracts/v1/management.openapi.yaml"
  make check` and `make test-race` pass. Exact-source Darwin/arm64 build from
  pushed commit `d7c814b` is at `/tmp/liapoldus-cli-admission-d7c814b`, SHA-256
  `d7a54b304782b0932ba1e4e83c050a63ebfe9b94f8308813a81759d7c1a8993a`;
  Go build metadata confirms `vcs.modified=false`. Installation approval for
  this digest is pending.
- The currently installed `/Users/docup/go/bin/liapoldus` remains the approved
  build from commit `39845869c81a83c40d215702c5642a512893d36f`, SHA-256
  `68db41f10d7b566da8d6ff6e8f30c04d567c811811a96ab9f1d73d5800ab0bd9`.
  Latest typed runtime status: Core running, Echo and Server exited. The
  read-only `dev plan` succeeds at working-tree digest
  `sha256:c0ed4230629e8d35d3c725e6db1a8d44cfaa64c733351c4d3d62ddea8f2a2bac`,
  with current revision 95 for each service; typed service metadata reads
  return HTTP 503 while the service containers are stopped.
- The Project still contains only `/echo`; the temporary auth route was
  removed. Local catalog reads show all three synthetic test references
  revoked and no pending registrations or rotations. No successful secret
  grant, protected request, configuration ACK, or rotation retirement was
  observed. Production remains out of scope.

## Local secret rotation smoke after trust recovery — superseded status — 2026-10-18

- `/Users/docup/go/bin/liapoldus` is now the approved Darwin/arm64 build from
  CLI commit `39845869c81a83c40d215702c5642a512893d36f`, SHA-256
  `68db41f10d7b566da8d6ff6e8f30c04d567c811811a96ab9f1d73d5800ab0bd9`.
  After explicit approval, local Core TLS assets were backed up and a new
  local CA plus target/service certificates were issued; OrbStack's selected
  Engine now runs Core, Echo and Server.
- Fresh typed `runtime status` and `service status server` confirm Server
  1.1.11 ready/acknowledged at generation 95. Its active configuration reports
  schemaVersion 1, while the Project settings file remains v2 with only
  `/echo`. `curl` to the published `/echo` route returned HTTP 200.
- `service secret list --target local --service server` returns two active
  synthetic references, one revoked record and one staged rotation. These are
  safe metadata only; the replacement has not been selected in a saved Project
  snapshot and no grant or rotation ACK has reached Server.
- The remaining live acceptance depends on the Studio WebView retry with exact
  host digest `88a4d2b0939f2427…`; that launch approval is pending. Then verify
  selected Project snapshot materialization, apply success/rejection handling,
  authenticated HTTP, replica ACK, revoke ordering and restart recovery. Clean
  up only the approved synthetic route/references. Production remains out of
  scope.

## macOS Docker context-aware Engine start — 2026-10-18

- Commit `39845869c81a83c40d215702c5642a512893d36f` is pushed to
  `origin/main`. On macOS, automatic Engine start now reads the selected Docker
  context and endpoint, honors `DOCKER_CONTEXT` over `DOCKER_HOST`, starts
  OrbStack for the OrbStack context/socket, and starts Docker Desktop for its
  context/socket. Unknown contexts fail closed without switching contexts.
- Regression cases cover OrbStack and Docker Desktop contexts/endpoints,
  `DOCKER_HOST`/`DOCKER_CONTEXT` precedence, the local Docker socket, and an
  unknown remote context. `GOWORK=off GOFLAGS=-p=1 go test
  ./internal/infrastructure/docker -count=1` passes.
- Full owner gates pass after the change:
  `GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-p=1
  LIAPOLDUS_CORE_OPENAPI="/Users/docup/Projects/Liapoldus Engine/core/contracts/v1/management.openapi.yaml"
  make check` and
  `GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-p=1 make test-race`.
- Built clean macOS/arm64 candidate from this source commit:
  `/tmp/liapoldus-cli-orbstack-2026-10-18`, SHA-256
  `68db41f10d7b566da8d6ff6e8f30c04d567c811811a96ab9f1d73d5800ab0bd9`;
  `go version -m` reports revision `39845869c81a83c40d215702c5642a512893d36f`
  and `vcs.modified=false`. It has not been selected or installed in Studio.
- Local OrbStack context inspection resolves to
  `unix:///Users/docup/.orbstack/run/docker.sock`. This confirms the selector
  will choose OrbStack on this host; it does not resolve the separate missing
  Core TLS bootstrap files. No target profile, certificate, Project, runtime,
  or secret state changed.

## Initial macOS local runtime retry (historical) — 2026-10-18

- Studio's approved Run action was retried after starting OrbStack. `docker
  info --format '{{.ServerVersion}} {{.Name}}'` reports `29.8.2 orbstack`, and
  `liapoldus target inspect local` reports a configured local runtime. The
  retry advanced past Docker startup but failed with
  `Core start: Core Management TLS certificate is unavailable`.
- `liapoldus runtime status --target local` reports the Core container exited.
  The `local` target's seven typed bootstrap TLS file references are all
  missing, so `coreContainerRequest` correctly refuses to create the Core
  container. The CLI did not generate replacement certificates, update the
  target profile, or alter the Project.
- The local Project settings file still has only the `/echo` route and no
  secret reference. No configuration candidate reached Core or Server; secret
  rotation ACK and retirement remain unverified. Restore the existing Core
  trust assets before resuming the authorized local flow; do not generate a
  replacement CA as an implicit repair.

## Pre-recovery local CLI and staged secret rotation (historical) — 2026-10-18

- `/Users/docup/go/bin/liapoldus` hashes to
  `c33d2114808708be062fb23877da3d48a8c803203ccd3d6be06d2be1227082ae`; Go
  build metadata identifies source commit `e297841`. Typed
  `service secret list --target local --service server` now returns two active
  opaque references, one revoked record, and one pending rotation in `staged`
  state. This supersedes the earlier no-pending-rotation snapshots below.
- The synthetic replacement was staged through `--secret-stdin`; its value is
  held in the local OS vault and was not rendered in command output or placed in
  argv. Core holds rotation/reference metadata only. Neither reference is
  present in Project settings or the active runtime configuration.
- The latest `runtime status --target local` reports Core running while Echo
  and Server containers are exited. Consequently `service status server
  --target local` currently returns Core HTTP 503 `service_unavailable` for
  service metadata. The last successful read before the containers exited
  reported Server 1.1.11 at generation 94, ready and acknowledged, with the
  existing schemaVersion 1 active envelope; Project settings remain v2 and were
  not applied.
- Project `services/server/settings.json` still contains only `/echo` and no
  secret reference. No candidate config was delivered to Server; rotation ACK,
  HTTP auth, restart recovery and old-reference retirement remain unverified.
- `dev plan --project <Server Echo Studio> --target local` was read-only and
  previously returned `plan.valid: true` for working-tree digest
  `sha256:5dee37ca31227ee1fbd1523f25cfb3c61f82d9aafffc934209fadca44f2a17f2`.
- Targeted source lifecycle regressions re-run on 2026-10-18:
  `GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-p=1 go test
  ./internal/presentation/cli -run
  'TestServiceSecret(Create|Revoke|Rotate)|TestDevApplyStagesOnlySelectedProjectSecretsAndPreservesOldValuesOnRejection|TestMaterializeServiceSecretMountUsesOnlySelectedProjectReferences'
  -count=1` passes. This verifies source behavior, not a live secret write.

## Initial read-only local secret catalog preflight — 2026-10-18

- Directly invoked Darwin/arm64 candidate
  `/private/tmp/liapoldus-cli-20261018-4516447`, SHA-256
  `5e30a42a1bcf5af1d3c6ac4131fa71a109ca3c234650e31443eaf28ef9ce8829`.
  `runtime status --target local` reports Core, Echo and Server running;
  `service status server --target local` reports active Server 1.1.8, digest
  `sha256:5a8e02a65624441b4654eda80103e7e91ea9406f069236f12ce2e128ae6807b2`,
  generation 92 and ready.
- Typed `service secret list` for both `server` and `echo` succeeds through
  Core and returns valid empty `service-secret-references/v1` catalogs with no
  pending registrations or rotations. This supersedes earlier live-preflight
  notes that the catalog route returned 404; it does not identify the running
  Core binary digest or prove secret creation, grant delivery, rotation, apply,
  or revocation.
- These commands were read-only. No secret value, Project configuration,
  runtime configuration or vault entry was created or changed. This direct
  candidate invocation does not establish which CLI path Studio currently
  selects; packaged host acceptance remains open.
- Re-ran the targeted source regression:
  `GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-p=1 go test
  ./internal/presentation/cli -run
  'TestServiceSecret(Create|Rotate|Revoke)|TestDevApplyStagesOnlySelectedProjectSecrets'
  -count=1` passes. It does not exercise a live secret write or retirement.

## Exact-source current macOS CLI candidate — 2026-10-18

- Built Darwin/arm64 CLI from published source HEAD
  `4516447e8b5fd964d631729de50f24da95b75233` to
  `/tmp/liapoldus-cli-20261018-4516447`, SHA-256
  `5e30a42a1bcf5af1d3c6ac4131fa71a109ca3c234650e31443eaf28ef9ce8829`.
  `file` confirms Mach-O arm64; `go version -m` records the exact revision and
  `vcs.modified=false`.
- On this HEAD, `GOWORK=off make check` and `GOWORK=off make test-race` pass.
- Studio still selects `/Users/docup/go/bin/liapoldus` at SHA-256
  `f04d7b013f11cbf5848ba80291bb8b2b85b5dbeaf40f2cb97ae319b38c3419d1`, which
  does not support service-secret commands. The candidate has not been selected
  or installed; exact-digest Studio confirmation and live local acceptance are
  open. No Project, Core, runtime or secret state changed during preparation.

## Exact-source macOS candidate for current Studio workflow — 2026-10-18

- Built the Darwin/arm64 CLI from published source HEAD `11b9a21a34ba42ad3324e1a8418152ee587937c6` to `/tmp/liapoldus-cli-current` with `GOTOOLCHAIN=go1.26.0 GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /tmp/liapoldus-cli-current ./cmd/liapoldus`.
- SHA-256: `b686f50ce14f1868f7272627aaaef507e546b15e6d862a0471b790ca794f8d63`. `file` reports Mach-O arm64; `go version -m` records the exact revision and `vcs.modified=false`.
- Current source gates `GOWORK=off make check` with the Core OpenAPI contract and `GOWORK=off make test-race` passed after the Admin Surface digest fence was added. The local Docker read-only inventory shows Core, Echo and Server containers running; the candidate has not been selected or run, and no Project, runtime or secret state was changed during this build.
- Fresh targeted verification: `GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-p=1 go test ./internal/presentation/cli -run 'TestServiceSecret(Create|Rotate|Revoke)|TestDevApplyStagesOnlySelectedProjectSecrets' -count=1` passed.
- Exact-digest selection and the local secret create/list/rotate/apply/ACK/revoke flow still require the normal Studio host trust and live acceptance.

## Current local CLI candidate and owner checks — 2026-10-18

- `GOWORK=off make check` and `GOWORK=off make test-race` both pass on the
  current CLI source code at commit `52cda34`.
- The current Darwin/arm64 CLI candidate was built from the exact current
  source commit `bedd9ba` at `/tmp/liapoldus-cli-2026-10-18-bedd9ba`, SHA-256
  `06ea0d658d160517a0ba899c096974cff1e9d70cd16fd0ed3be4a2265390c788`.
  `file` confirms Mach-O arm64 and `go version -m` records revision
  `bedd9ba8ece579cda09bcb47333a1abae16d8167`, `vcs.modified=false`. The
  previous `e0a60a59…` candidate is superseded. This candidate has not been
  selected, installed or run; exact-digest Studio trust is pending.
- Studio still selects `/Users/docup/go/bin/liapoldus` at SHA-256
  `f04d7b013f11cbf5848ba80291bb8b2b85b5dbeaf40f2cb97ae319b38c3419d1`, which
  reports `unsupported service command` for the secret catalog operation. The
  Core live endpoint is also absent from the running old Core binary. No target,
  Project, runtime or secret state changed during these preflights.

## Secret cleanup retry after Core revocation — 2026-10-18

- [x] A local cleanup failure after Core has revoked a reference can now be
  retried safely. CLI rechecks the current Project and every replica's exact
  digest/generation/readiness ACK, recognizes the Core `revoked` metadata state,
  skips a duplicate Core revoke, then idempotently clears the runtime copy,
  local reference index and OS vault entry.
- [x] Regression simulates a Core revoke followed by unsafe local staging entry,
  verifies that vault/index data are preserved on the first failure, then fixes
  the entry and confirms a retry completes cleanup without a second Core revoke.
- [x] `GOWORK=off LIAPOLDUS_CORE_OPENAPI='/Users/docup/Projects/Liapoldus Engine/core/contracts/v1/management.openapi.yaml' make check`,
  `GOWORK=off make test-race`, and the focused secret-revocation suite pass.
  This is source-level failure recovery evidence; the local Core runtime
  rotation/timeout/restart acceptance is still open.

## Local service-secret reference lifecycle — 2026-10-18

- [x] Added typed Core Management API list/create/revoke calls for service-bound
  opaque references. The API request and response contracts reject unknown
  fields and never contain the secret value.
- [x] Added `liapoldus service secret list|create|rotate|register|revoke --target local
  --service <id>`. Create accepts secure stdin or a hidden terminal prompt,
  stores the value in the OS keyring and reports only safe reference metadata.
  It does not expose the value to Core until a Project configuration selects
  that reference. If Core registration fails, the reference and vault entry
  remain available for `service secret register` retry. Revoke requires
  `--confirm` and clears local copies only after Core accepts it.
- [x] Rotation creates a separate reference and records its source reference in
  local metadata. Listing reports staged registration and rotation states;
  it never revokes the previous secret automatically. Both Studio and CLI now
  verify that the saved Project no longer contains the reference and every Core
  replica is ready on the exact settings digest and applied generation before
  retirement. CLI snapshots the Project before checking and fails closed on a
  stale digest, missing ACK, unready replica, or unavailable metadata.
- [x] Studio provides host-owned secure input and a contextual reference picker.
  Plugin webviews receive only active opaque references and safe metadata; the
  secret value is sent directly to the local CLI over secure stdin.
- [x] On 2026-10-18, `GOWORK=off make check` and `GOWORK=off make test-race`
  passed. The end-to-end local rotation acknowledgement and crash/restart
  acceptance still require a live Studio/Core runtime and have not been run.
- [x] A fresh CLI `Runner` after a staged rotation reconstructs the pending
  rotation from the persisted local reference index and Core catalog without
  creating another reference, revoking the old one, or exposing the value. This
  covers CLI-process recovery; it does not replace the still-open live
  Studio/Core/runtime restart acceptance.
- [x] Added CLI regressions proving a reference still present in Project,
  mismatched settings digest, or one unready replica blocks Core revocation and
  preserves the local vault value. The successful path confirms each replica's
  exact settings digest, generation, and readiness before it calls Core.
- [x] Live preflight recheck on 2026-10-18: the `local` target is configured;
  Core, Echo, and Server containers are running. The selected
  `/Users/docup/go/bin/liapoldus` is still the old Darwin/arm64 build with
  SHA-256 `f04d7b013f11cbf5848ba80291bb8b2b85b5dbeaf40f2cb97ae319b38c3419d1`.
  `service secret list --target local --service server` returns
  `unsupported service command`. These read-only checks changed no Project,
  Core, runtime, or secret state.
- [ ] Live host preflight on 2026-10-18 found that Studio's selected
  /Users/docup/go/bin/liapoldus is stale: service secret list returns
  unsupported service command. The updated Darwin/arm64 candidate includes
  Project-scoped retirement checks from commit `f60ce65`; it is at
  /tmp/liapoldus-cli-2026-10-18-f60ce65, SHA-256
  `3901057bd6a2b279a03a2926eb8bae8648b4163ab1801dfc2a04bd1c563b55e6`.
  It reaches Core's catalog route, which returns HTTP 404 from the currently
  running Core binary. This exact CLI digest is not installed yet. Secret
  workflow remains unverified until Studio selects this binary, Core exposes
  the route, and the full rotation path passes.

- [ ] Rebuilt from current published HEAD `807ab1c` after confirming that its
  only code change since `7e7d607` is the CLI-process rotation recovery test.
  Darwin/arm64 candidate:
  `/tmp/liapoldus-cli-2026-10-18-807ab1c`, SHA-256
  `e2097e38a0e86c9749091588f8d0b0341a0c6fc84f5996382b5ad7cc5d9b1bff`.
  `file` confirms Mach-O arm64; `go version -m` confirms revision
  `807ab1c3fe8751ce4aa07757ce979be04725ac72`, `vcs.modified=false`.
  It has not been selected, installed, or run; its exact digest needs Studio
  trust before replacing the selected CLI. The previous `.606df…` candidate
  is absent from `/tmp` and superseded by this rebuild.
- [ ] Current Core source HEAD is `e35e66e`; the previously documented Linux
  candidate was built from `d7f82cd` and its temporary file is no longer
  present. The live Core container still lacks the secret catalog endpoint,
  so Core update and the full secret workflow remain unverified.

## Project-scoped local secret runtime materialization — 2026-10-18

- [x] `service secret create|rotate|register` now stores values in the OS vault
  and the local opaque-reference index without placing them in Core's bind mount.
- [x] `runtime start` captures `WORKING_TREE` as an immutable private Project
  snapshot before reading artifacts and settings. It stages only references
  found in that snapshot's service settings.
- [x] `dev apply` stages only references found in its immutable snapshot before
  Core validates the candidate. Existing mounted values remain available while
  the candidate is pending or rejected; after success, the mount is pruned to
  the applied Project references. Missing vault values, cross-service refs and
  malformed opaque references fail closed.
- [x] Added regression coverage that observes both old and candidate values
  during Core validation, confirms the previous value remains after rejection,
  and confirms it is pruned only after accepted configuration or retained after
  failed replica acknowledgement. Secret creation and failed registration
  tests assert that unselected values never reach the Core mount.
- [x] Direct `core restart` now accepts only an already-running Core container;
  after `core stop` cleans the private secret mount, callers must restart the
  selected Project through `runtime start` so the scoped references are restored.
- [x] `GOWORK=off make check` and `GOWORK=off make test-race` pass for this
  checkpoint. Live macOS/Core apply and replica-ACK verification still awaits
  the exact-digest native Studio trust flow and current Core runtime.
- [x] Built the exact-source macOS/arm64 CLI candidate for commit `bedd9ba`:
  `/tmp/liapoldus-cli-2026-10-18-bedd9ba`, SHA-256
  `06ea0d658d160517a0ba899c096974cff1e9d70cd16fd0ed3be4a2265390c788`.
  It is not installed or selected pending Studio's native trust confirmation.

## Lease-expiry recovery checkpoint — 2026-10-17

- The earlier `runtime.start` lease failure has been recovered through Studio
  without rotating credentials or certificates: a scoped Core-only stop/start
  cleared Core's volatile live replica directory, then typed `runtime.start`
  registered Echo and Server successfully. Current UI status reports all three
  containers running, and Core-backed service metadata is ready at generation
  68. `/echo?text=lease-recovery-20261017` returned HTTP 200; Studio Output
  confirms Echo logged the exact marker.
- CLI now emits safe code `service_replica_lease_expired` with an operator
  recovery explanation instead of a generic stderr diagnostic; focused
  presentation regression passed. The selected Studio CLI binary was not
  rebuilt for this source change. Automatic recovery after lease expiry remains
  deliberately unresolved: current Core/SDK contract requires a new
  certificate-bound incarnation, and CLI has no authorized certificate-issue
  operation. Do not infer that a Core restart is a general or automatic fix.

- [x] Исправить Core-only lifecycle: `core stop/restart` теперь адресуют только
  принадлежащий выбранному target `liapoldus-core`; сервисы, сеть и staging не
  затрагиваются. Добавлены тесты изоляции и отсутствующего Core-контейнера.

## Core settings inspection slice — 2026-10-17

- Extended the slice with Core `PUT /api/v2/settings` over the Management API
  using exact `If-Match` CAS, strict response validation and a separate narrow
  `CoreSettingsWriter` capability. `core settings update` accepts only a
  bounded `core-settings-patch/v1` request file with allowlisted scalar fields;
  it requires local target, matching revision, no pending revision and explicit
  `--confirm`. Existing filesystem references are preserved inside the CLI from
  the Core snapshot; no path/secret field is accepted by the patch or returned
  to Studio. Core restart is deliberately not yet part of this command.
- Focused tests cover exact PUT/ETag/body, invalid document rejection,
  scalar-only merge with path preservation, strict patch parsing, CAS and
  redacted JSONL result. Studio's typed Wails form/request-file bridge and the
  serialized Core restart/recovery operation remain open.

- Added `core settings inspect --target <id>` over the Core Management API.
  Output uses `core-settings-view/v1`, desired/effective revisions and ETag;
  TLS/secret mount paths are represented only by allowlisted logical target file
  references. Unknown mount paths fail closed as `unmapped`; no raw settings
  JSON or absolute path is written to Studio JSONL. The command is read-only.
- The endpoint currently used by the CLI is Core's implemented
  `/api/v2/settings`; the Core owner source now documents its GET/PUT CAS
  boundary in Management OpenAPI and API overview. Central docs sync/publication
  remains pending. Typed settings update, reference catalog/selection,
  restart-required operation/recovery, and resource publication remain
  unimplemented; do not imply this read-only slice closes the Core settings TODO
  below.
- Focused runner/coreapi tests verify target-vault auth, revision/ETag contract,
  logical reference projection, unknown path redaction, and no absolute-path
  leakage. `GOWORK=off make check` and `GOWORK=off make test-race` both pass
  for this increment. `git diff --check` is clean.

The CLI owns the typed automation boundary used by Studio. Overall product
architecture and acceptance are canonical in [`../studio/plan.md`](../studio/plan.md).
Do not duplicate UI/SDK/service product schemas here.

## Current UI-driven runtime blocker — 2026-10-16

- [x] Studio event writer now maps the terminal Service SDK error
  `service SDK replica lease expired` to typed code
  `service_replica_lease_expired` and a bounded recovery message: an operator
  must issue a new service identity/certificate; retrying the same incarnation
  cannot work. Focused CLI presentation regression passes. This improves
  diagnosis only; it does not rotate credentials or recover the runtime.
- From Studio Project `Server Echo Studio`, target `local`, one typed
  `runtime.start` using the already trusted Echo 1.2.1 and Server 1.1.6 pins
  failed. JSONL diagnostics: both containers exited with
  `service SDK replica lease expired`; Core registration of Echo then timed out
  at `POST /api/services/echo/replicas/register`. A subsequent typed
  `runtime.status` showed only Core running/reachable. Do not retry `runtime.start`
  blindly.
- Source confirms this is a terminal expired-incarnation refusal: Core's replica
  directory returns `lease_expired` when the prior lease for the same
  certificate-bound incarnation has expired; Service SDK intentionally does not
  mint a new identity. Current target configuration references external service
  certificates, and Studio/CLI has no typed per-replica certificate rotation
  operation. The next safe implementation/design gate is an operator-controlled
  new service incarnation/certificate flow (or an explicitly supported Core
  expired-entry recovery contract), followed by a single UI-driven retry. Do not
  mutate CA material or persistent target references as an implicit recovery.

## Current inventory — 2026-10-13

### Core Docker bootstrap mount correction — 2026-10-16

- Studio evidence: selected target `local` had a configured credential, but Core
  was stopped; `core.start` had failed with Docker `bind source path does not
  exist`. The then-selected CLI redacted the host path, so the precise missing
  mount was not yet observable through Studio.
- Root-cause hypothesis from the current implementation: the one-time bootstrap
  token directory was created under the OS temp root (`os.MkdirTemp("")`), which
  on macOS commonly resolves under `/var/folders`; OrbStack's Docker VM may not
  expose that host location. This is distinct from the Core state directory,
  staged binary and TLS mounts.
- Changed bootstrap-token staging to create the 0700 directory inside the already
  mounted Core state directory. Core receives the token file through that
  existing mount; there is no separate bind mount from system temp. The file
  remains 0600, is removed after startup, and its value stays out of environment,
  logs and inspect metadata.
- Focused regression verifies the token path is under the mounted state root,
  that no `/run/secrets` host mount is emitted, and that token directory cleanup
  still occurs. `GOWORK=off make check` and `GOWORK=off make test-race` pass.
- The Studio JSONL writer now promotes the allowlisted
  `docker_mount_source_unavailable` code from wrapped stderr into the typed event
  `code` field with error severity, while path/credential redaction still applies
  to the human message. Focused writer/redaction tests and the full CLI
  presentation package pass. This makes the next UI retry show a useful typed
  diagnosis instead of generic `cli.stderr`.
- This patch is source-only. It has not been built into the CLI selected by
  Studio, and Core was not restarted. Current candidate built:
  `/tmp/liapoldus-cli-diagnostic-path.hT6h30/liapoldus`, Mach-O arm64, SHA-256
  `0b9948e408932c552883d2aefd5e7134b96dc51e8b104161c3ef461a468c1319`.
  It includes bootstrap staging inside the Core state mount, typed stderr event
  forwarding, and canonical macOS symlink-path matching for safe mount-role
  diagnostics (regression passes on macOS; Windows test binary cross-compiles).
  Earlier hashes `a2f5e96e...` and `864667a6...` are stale. The
  current candidate is not selected or run. Next: select/review this artifact in
  Studio, perform one Core start, then verify `core.status` and continue the
  UI-driven vertical acceptance. Preserve the current Core DB, vault and
  certificates.

- [x] `service artifact` owns local binary registration/trust metadata.
- [x] `service inspect|manifest|schema|status` routes metadata reads through
  Core Management API. CLI source has no direct Service SDK client.
- [x] `service resource list` resolves manifest-declared service-admin catalogs
  via Core Management API and returns only bounded `value/label` fields. CLI has
  no service endpoint URL or Service SDK REST path.
- [x] Studio-facing target, credential, artifact and runtime-plan responses use
  explicit allowlisted DTOs. They do not serialize credential references,
  executable/project paths, TLS/bootstrap configuration or local Docker
  inspection environment/mount data.
- [x] Docker runtime planner starts staged, operator-built service binaries;
  Docker build/image compilation is outside the CLI contract.
- [x] Core binary update is a single confirmed typed command: it verifies the
  reviewed Linux ELF digest/platform, starts the candidate, and only commits
  its path/digest to the CLI target registry after Core Management is healthy.
  On startup failure the previous registry entry remains unchanged; Core may
  need recovery with `core start`. If registry commit fails after start, CLI
  stops/removes only the candidate Core container. SQLite migrations are not
  rolled back. Latest native candidate built from this source:
  `/tmp/liapoldus-cli-binary-update-final.r0EBnR/liapoldus`, SHA-256
  `41a2e26d0ee12915a4f218aafcd4e8c85b5ce1a71d65336f4cb2ef5f36acdbfa`;
  it has not been selected in Studio and no live Core/Docker operation was run.
  CLI rebuilt as `/tmp/liapoldus-cli-core-update.a1LCc8/liapoldus`, SHA-256
  `603dce5506c4106d05e24f45ff617e29f6594a5777ebfedeb3910fcc86deb070`;
  user approved the exact digest and selected it in Studio. Studio's new Wails
  build exposes the update UI. Through Studio, the local target was observed
  read-only with Core Management reachable and Core/Echo/Server containers
  running. The older `/tmp/liapoldus-cli-auto-core-update` binary rejected
  `core binary inspect` with `invalid argument "inspect"`; the corrected CLI
  from current source was approved and selected in Studio at
  `/tmp/liapoldus-cli-core-update-fixed.wWB1gi/liapoldus`, SHA-256
  `03f414153806974300f174ced195b2eb2c3d0af1d3377d7a308b28dfe98312d2`.
  `core binary inspect` now succeeds through the Studio UI. A Core candidate was
  inspected through the UI as Linux/arm64,
  SHA-256 `71803f1e0c85d699dbb8a6aeb46d134cc5777e2302110a32ea949b48465eda86`.
  User approved that exact Core digest. The UI update attempt stopped before
  operation execution because the selected binary returned
  `core binary inspect|set is required`; current source correctly implements
  `inspect|update`. No Core/runtime state changed. Current-source candidate
  `41a2e26d0ee12915a4f218aafcd4e8c85b5ce1a71d65336f4cb2ef5f36acdbfa` is
  verified at the recorded path above; it still needs explicit digest trust in
  Studio before it can replace the selected CLI. Then rerun the approved Core
  update through Studio and verify Core/service state and metadata provenance.
- [x] `runtime start` reconciles stale target-labeled service containers: it
  removes only expected service containers in `created`, `exited` or `dead`
  states, after verifying every relevant state. Running/unknown containers block
  Start without partial cleanup. Core and persistent state are preserved.
- [~] Studio stack Stop now shows an accessible RU/EN confirmation naming the
  exact selected target and disclosing that its runtime containers/staging are
  removed while Project/Core DB/credentials/source binaries remain unchanged.
  Model coverage and the full Studio gate pass; native click-through plus a
  Docker stop/start acceptance is still pending.
- [x] Studio reads live service manifest/configuration schema through typed CLI
  responses backed by Core Management API; live identity/readiness are shown
  separately from Project-owned desired settings and replica convergence.
  Editing remains fail-closed when Core's authenticated release provenance does
  not match the Project artifact binding.
- [~] Added local-only `dev plan/apply` with private Git-backed snapshots,
  content-addressed `working-tree:<bundle-digest>` revisions, source-checkout
  preservation, secret/.studio exclusion, symlink rejection and size limits.
  CLI focused tests cover draft settings/links, deletions and no Git mutation.
  Studio `Save & apply` now invokes this typed intent. The remaining requirement
  is a serialized/coalescing operation queue with cancellation, bounded progress,
  debounce-driven apply and explicit recovery confirmation. Generated dev apply
  idempotency keys now bind the exact bundle to Core's current per-service
  configuration revisions: same-revision retry reuses the key, while a later
  apply after Core advances a generation gets a new key. Regression covers both;
  standalone `make check` and `make test-race` pass.
- [~] Added the durable cross-process local `dev apply` queue: OS file locks
  serialize callers per target; a private atomic journal stores only Core scope
  and service idempotency keys; Core `POST /api/operations/resolve` recovers
  accepted operation IDs after a CLI crash, and the next draft waits for terminal
  states. `degraded` or unrecognized state fails closed. Focused journal, Core
  client, runner recovery, and Core Management integration tests pass. Remaining:
  explicit operator reconciliation UX for a durable degraded operation and
  full Core+CLI+Studio process-crash E2E on macOS/Windows/Linux.
- [x] Terminal failed recovery no longer silently clears the journal and moves
  into a new apply. The operator must inspect Core's operation report and pass
  typed `--retry-failed` together with `--confirm`; degraded/unknown operations
  remain fail-closed because desired state may already have been promoted.
- [ ] Define Core-backed typed resource metadata/publication and restart-required
  local Core settings/restart workflow; keep credentials in operator storage.
- [~] 2026-10-16 focused CLI test
  `TestServiceResourcePublishUsesTypedCoreClientAndProjectScopedFiles` passes:
  publication is sent as a typed CLI intent through the Core client with
  Project-scoped inputs. This does not establish the complete Studio→CLI→Core→
  service publication operation, durable Project resource references, recovery,
  or restart-required Core settings.
- [~] Preserve replica-level desired/applied/health outcomes and partial-rollout
  semantics in operation/report responses. The CLI now strictly decodes the
  Core `Operation` contract, rejects unknown fields and invalid/duplicate
  replica outcomes, and emits only operation ID, state, and bounded
  `replicaOutcomes` to Studio. Core `Apply` responses are also strict-decoded
  and reconstructed from only `operations`/`requestId`, so unknown Core fields
  cannot be forwarded. `dev apply --wait` aggregates outcomes and keeps them
  when Core reports `failed`/`degraded`, while leaving the recovery journal
  intact; it does not claim rollback. Focused Core API, application, and CLI
  tests pass; `GOWORK=off LIAPOLDUS_CORE_OPENAPI=<absolute Core OpenAPI>
  make check` and `GOWORK=off make test-race` pass. Remaining: preserve
  health/readiness as a separate per-replica view, cover several operations in a
  single apply/report, and run the full cross-repository Studio/Core integration
  acceptance.
- [x] Add CLI architecture regression proving no Service SDK/Wool dependency or
  private `/_liapoldus/` REST route is present in Go source; Core-backed
  `service manifest|schema|status` client tests assert only canonical
  `/api/services/...` Management API paths.
- [~] Core owns and tests Core→Service SDK REST lifecycle/metadata; direct
  Server→Wool→Echo E2E without CLI/Core in the business-call path remains open.
- [x] 2026-10-14: standalone `GOTOOLCHAIN=go1.26.0 GOFLAGS=-p=1 GOWORK=off
  make check` and `make test-race` pass; `go test ./internal/infrastructure/coreapi`
  also passes with `LIAPOLDUS_CORE_OPENAPI` pointed at the current local
  `core/contracts/v1/management.openapi.yaml`.
- [x] 2026-10-15 local Docker acceptance: Studio restarted only the bound Echo
  service `1.2.1` (`linux/arm64`, Project digest
  `65143cb9c15bad0a72d4104a4fbe062cd51db4c857c384e4b6678bce26340ecf`). Core
  reported readiness; ordinary curl through Server returned HTTP 200 and
  `echo: approved-echo-1.2.1`; Studio's Core-backed service logs showed the
  exact query text on Echo stdout. The artifact was already registered and
  selected as the Project binding; no source or Project files were edited in
  this verification. Endpoint creation through Server StudioPlugin and
  cross-platform runtime acceptance remain separate open Studio/release gates.

### Admin Surface digest normalization — 2026-10-16

- Reproduced the current Studio UI error `bridge.admin_surface_digest_invalid`
  on the Server admin surface while ordinary Core-backed runtime/status and
  `/echo` traffic remain independently healthy.
- Core serializes the live service digest with one `sha256:` prefix. The CLI
  response adapter now normalizes that canonical value before constructing the
  typed Admin Surface result; regression coverage rejects accidental duplicate
  prefixing. Targeted tests passed:
  `go test ./internal/infrastructure/coreapi ./internal/presentation/cli`.
- Native candidate built from this source:
  `/tmp/liapoldus-cli-admin-surface.mGgwuw/liapoldus`, macOS/arm64, SHA-256
  `8108f74f775681f7c218875ca8cd015167b354623e07e52e1e2df80c827bb20b`.
  It has not been selected or run by Studio. The currently selected CLI and all
  target/Core/runtime state remain unchanged. Next verification is to select
  this exact candidate through Studio's native trust flow, reload the Server
  Admin Surface and confirm the same Project artifact provenance; do not loosen
  Studio's digest validation to mask the old CLI response.

Verification after the working-tree snapshot implementation and the fresh
2026-10-14 standalone gates above. The CI workflow's Core pin is still a
separate release-integration concern; no pin was changed because owner changes
are unpublished. See `AGENTS.md` for the no-commit/no-push boundary.

### Current operation-result candidate — 2026-10-16

- Core `Operation` and `Apply` responses are now strict-decoded against their
  typed Core contract and reconstructed from allowlisted fields. Replica
  outcomes survive both success and failed/degraded `dev apply --wait` results;
  the recovery journal remains intact on partial failure.
- Focused CLI/Core tests, Studio consumer model tests (19/19), full CLI `make
  check` with the owner's OpenAPI contract, and `make test-race` pass.
- Current macOS/arm64 binary candidate:
  `/tmp/liapoldus-cli-operation-safe.42SCNz/liapoldus`, SHA-256
  `30421a6e30640ef089322d4a1878c28b7edda8fda8967968148b12e5a63be10b`.
  This includes the earlier Admin Surface digest fix and supersedes candidate
  `8108f74f...` for future Studio review. It has not been selected or run.


### Runtime start and configuration apply — 2026-10-17

- `runtime start` now reconciles already-running containers without destroying them, re-registers their current SDK lease, and retains created containers/logs when registration fails. Focused tests, `make check`, and `make test-race` pass; the installed CLI remains the user-approved SHA-256 `f04d7b013f11cbf5848ba80291bb8b2b85b5dbeaf40f2cb97ae319b38c3419d1`.
- The local `Server Echo Studio` target has already recovered through typed CLI and both services reported running; repeated Run accepted Core registration. Final smoke with updated Core/Server/Echo binaries remains pending their exact digest gate. Source commit/push pending.

### Generic Admin Surface tables and actions — 2026-10-18

- Added typed `service admin query` and `service admin invoke` commands. Every
  query/action re-reads the live descriptor, checks its exact SHA-256 binding,
  validates the declared table/action and bounds the request/response. Queries
  return only descriptor-allowlisted scalar columns; actions read input from a
  Project-scoped JSON file, require explicit confirmation and return only a
  completion marker to Studio.
- Regression tests cover live digest binding, pagination, declared action input,
  CAS, idempotency, malformed/trailing JSON, column filtering and preventing
  service response fields from reaching the plugin. Focused tests and full
  `make check` with the current Core OpenAPI contract pass. Race check and
  Studio/WebView integration remain pending.

### Local config admission follow-up — 2026-10-19

- `DevelopmentDeploymentService.Apply` now always reads Core's typed plan before
  preparing local secrets or writing its recovery intent, even when Studio
  supplies an idempotency key. This prevents a caller-provided key from bypassing
  current admission blockers. A regression test proves blocked plans stop before
  secret staging, recovery persistence, or `Core.Apply`.
- `GOWORK=off GOFLAGS=-p=1 LIAPOLDUS_CORE_OPENAPI=<absolute Core OpenAPI>
  make check` and `GOWORK=off GOFLAGS=-p=1 make test-race` pass.
- In `Server Echo Studio` / `local`, the approved Core build
  `df625da7910770c082db1364c0f9f0d149a15d487383af4a43e8aa8fb7607420` is
  running. `dev plan` for working-tree digest
  `c0ed4230629e8d35d3c725e6db1a8d44cfaa64c733351c4d3d62ddea8f2a2bac` is valid
  at Echo/Server revision 96 and reports no `blockedBy` items. After
  `runtime start --revision WORKING_TREE`, both service containers are running
  and report ready with desired/applied generation 96. Server 1.1.11 and Echo
  1.2.2 acknowledge the exact Project settings digests; `GET /echo?text=local-smoke`
  returns HTTP 200, and omitting required `text` returns HTTP 400.
- The first `runtime start` attempt omitted the required `--revision` and failed
  before runtime changes. Repeating with explicit `WORKING_TREE` reconciled the
  stale containers and registered both replicas. The old HTTP 409 has not been
  reproduced, but its exact historical cause remains unknown.
- All three previous synthetic Server references remain revoked and the Project
  still has no secret references. Studio's Server secret form is prepared with
  a non-secret label; manual secure entry and submission are awaiting the user.
  No successful live auth grant, rotation, or retirement ACK has been proven.
