# CLI — Studio configurator contract and runtime TODO

## Local service-secret reference bridge — 2026-10-17

- [x] Added typed Core Management API list/create/revoke calls for service-bound
  opaque references. The API request and response contracts reject unknown
  fields and never contain the secret value.
- [x] Added `liapoldus service secret list|create|revoke --target local
  --service <id>`. Create accepts secure stdin or a hidden terminal prompt,
  stores the value in the OS keyring, stages it with mode `0600` under the
  target-owned Core `secrets/` directory, and reports only safe reference
  metadata. Core rejection removes the staged file and vault entry. Revoke
  requires `--confirm` and clears local copies only after Core accepts it.
- [x] On 2026-10-17, `GOWORK=off make check` and `GOWORK=off make test-race`
  passed with `LIAPOLDUS_CORE_OPENAPI` pinned to the current Core source contract.
- [ ] Studio's typed native secure-input bridge and settings UI remain open;
  plugins must receive opaque references only.

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
