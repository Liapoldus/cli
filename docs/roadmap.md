# Roadmap и acceptance criteria

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

### Slice 3a — Core v2 bootstrap boundary

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

### Slice 6 — Core cutover

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

1. Финальное имя бинаря и package/module path.
2. Формат generated Core API client.
3. Конкретная schema canonicalization library.
4. Набор remote execution adapters и trust model SSH/agent.
5. Secure provider для desktop и GitHub Actions.
6. Политика signed commits и provenance attestations.
7. Гранулярность service-owned schema validation.
8. Формат cross-repository contract test fixtures.
