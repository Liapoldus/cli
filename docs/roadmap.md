# Roadmap и acceptance criteria

## Production-ready цель

CLI считается production-ready только после прохождения всех gates ниже:

1. Слои domain/application/infrastructure/presentation разделены, composition
   root единственный, а public command contract не расходится с executable.
2. Bundle строится только из указанной immutable Git revision; digest
   воспроизводим, secrets исключаются, schema validation и diagnostics typed.
3. Core API client имеет timeout, cancellation, authentication, idempotency,
   typed HTTP/contract errors и operation observation без потери контекста.
4. Local, API-only remote и несколько targets изолированы; SQLite доступен
   только поддержанному local backup/restore adapter и никогда не remote Core.
5. Git source-of-truth policy, commit/clean-tree/approval gates и CI workflow
   проверяются одинаково локально и в GitHub Actions.
6. Есть unit, integration, contract, race, vet, static analysis и end-to-end
   tests для успешного, failed, degraded, timeout и conflict сценариев.
7. Есть воспроизводимые release artifacts, versioning, checksums, install path,
   rollback guidance и security review без секретов в логах или reports.
8. Документация, examples и generated command contract соответствуют коду; все
   required checks проходят на поддерживаемых ОС.

Текущий этап — hosted conformance и release publication после contract
hardening. Локальные release artifacts и credentials boundary уже проверены;
остаётся выполнить эти gates на чистом GitHub runner.

## Фактически закрыто в архитектурном срезе

- удалён монолитный `internal/cli` и его дублирующий command implementation;
- добавлены domain ports и application use cases для bundle/deploy/operations;
- Git adapter читает tree и service settings только из resolved commit SHA;
- Core API, target registry и local Core process lifecycle изолированы
  infrastructure adapters;
- presentation получает зависимости через composition root в `cmd/liapoldus`;
- добавлены `target add/test`, `operation get/watch`, `status`, `bundle inspect`,
  `doctor`, expected-generation и version output;
- добавлены Makefile, CI quality gates, race/vet/staticcheck checks и GoReleaser
  cross-platform configuration.
- добавлены `CredentialProvider`, `--token-stdin`, pinned GitHub Actions и
  Core mTLS lifecycle smoke workflow;
- GoReleaser `check` и snapshot release с пятью platform binaries, archives и
  checksums успешно выполнены локально.

Это ещё не release approval: hosted cross-repository contract/conformance suite
и release publication остаются обязательными gates. Локальный Core walkthrough
уже проверен: bootstrap через target references, запуск, TLS/mTLS health через
typed adapter и остановка. Core HTTP surface изолирован типизированным adapter
и проверяется against pinned owner OpenAPI contract в CI.

## Vertical slices

### Slice 1 — project/revision resolver

- открыть существующий Studio Project;
- разрешить commit SHA;
- проверить clean revision и deterministic file ordering;
- вернуть machine-readable diagnostics.

### Slice 2 — bundle builder

- schema-driven validation;
- environment resolution;
- secret references;
- canonical bytes и digest;
- golden vectors.

### Slice 3 — Core API client

- capabilities;
- validate/plan;
- apply с idempotency и CAS;
- operation watch;
- typed errors и exit codes.

### Slice 3a — Core v2 bootstrap boundary (закрыт)

- Core auto-initializes SQLite from `CORE_INIT_*` exactly once;
- CLI supplies bootstrap environment only when starting a local Core;
- legacy `core-migrate` orchestration moves behind the CLI target adapter;
- no Core CLI entrypoint, SQLite writer or internal-package import remains;
- startup and first-admin bootstrap are covered by cross-repository tests.

### Slice 4 — targets и local lifecycle

- target profiles;
- local Core start/stop/status/logs;
- API-only remote target;
- isolated credentials.

### Slice 5 — CI и approval

- GitHub Action example;
- exact SHA checkout;
- environment approval evidence;
- signed machine-readable report.

### Slice 6 — Core cutover (закрыт)

- удаление Core CLI;
- удаление старых docs/tests/contracts;
- cross-repository conformance gate.

## Acceptance criteria

- `apply` невозможен без immutable commit;
- одинаковый input даёт одинаковый bundle digest;
- CLI не открывает Core SQLite;
- Core принимает bundle атомарно;
- повторный apply с тем же idempotency key безопасен;
- local/staging/production изолированы;
- Studio не делает Core API requests;
- remote approval нельзя обойти CLI flag;
- degraded rollout возвращает отдельный non-zero result;
- CI и локальный запуск используют один command contract;
- после cutover в Core нет CLI packages, routes и документации;
- secrets отсутствуют в Git, bundle reports и logs.

## Открытые решения

1. Набор remote execution adapters и trust model SSH/agent: текущий scope
   ограничен API-only remote target.
2. Secure provider для desktop и GitHub Actions: `CredentialProvider` boundary
   и environment/CI adapter реализованы; desktop keychain backend остаётся
   отдельной implementation choice.
3. Политика signed commits и provenance attestations.
4. Гранулярность service-owned schema validation.
5. Формат cross-repository contract test fixtures и hosted execution.
