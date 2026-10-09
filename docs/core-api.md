# Core API contract для CLI

## 1. API-only boundary

CLI не импортирует внутренние Go packages Core и не открывает SQLite. Между
репозиториями существует versioned Management API contract, включающий:

- capabilities и API version discovery;
- bundle validation/plan;
- apply request;
- operation status and events;
- service generations, replicas, leases и readiness;
- generic Core health;
- безопасные audit/provenance fields.

OpenAPI/JSON schemas должны публиковаться владельцем Core API. CLI использует
generated client или строго типизированный transport adapter; ручные копии
DTO между репозиториями запрещены.

## 2. Apply request

Концептуальный request содержит:

```yaml
project:
  id: forms-platform
  repository: https://github.com/example/forms-platform.git
  revision: 0123456789abcdef
bundle:
  schemaVersion: core-config/v2
  digest: sha256:...
  services: []
  links: []
target:
  environment: production
  expectedGeneration: 41
request:
  idempotencyKey: ...
  actor: ci/github-actions
```

Это illustrative shape, а не финальный wire schema. Product-specific service
settings остаются opaque для Core с service-owned schema validation.

## 3. Plan before apply

CLI должен уметь:

1. получить capabilities Core;
2. отправить bundle в read-only validation/plan endpoint;
3. получить semantic diff и warnings;
4. проверить expected generation/CAS;
5. запросить apply только после явного `--confirm` или CI policy.

`plan` не создаёт durable desired state и не запускает replica rollout.

## 4. Atomicity and idempotency

Core обязан атомарно записать bundle metadata и desired configuration в SQLite.
Если request повторён с тем же idempotency key и digest, CLI получает исходную
operation. Если key используется с другим digest, Core возвращает conflict.

CLI обязан сохранять связь:

```text
commit SHA → bundle digest → Core operation ID → generation → replica result
```

## 5. Observability

Core не принимает от CLI инструкции по запуску отдельных service processes.
CLI читает operation, generation, readiness, lease и replica ACK, но не
подменяет их локальной оценкой. `degraded` является отдельным неуспешным
результатом для CI, пока policy явно не разрешает продолжение.

## 6. Compatibility

До apply CLI проверяет API version, bundle schema, required capabilities и
compatibility metadata. Несовместимость обнаруживается до durable write.

Core API не должен знать о Studio. Studio может показывать CLI reports, но не
становится скрытым API proxy.
