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

Текущий request содержит:

```yaml
project:
  id: forms-platform
  repository: https://github.com/example/forms-platform.git
  revision: 0123456789abcdef
bundle:
  schemaVersion: core-config/v2
  digest: sha256:...
  services:
    - id: forms
      settings: { opaque: true }
  links: []
target:
  environment: production
  expectedGeneration: 41
request:
  idempotencyKey: ...
  actor: ci/github-actions
```

Это versioned wire shape. Product-specific service settings остаются opaque для
generic Core envelope; schema validation принадлежит service contract.

## 3. Plan before apply

CLI умеет:

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

## Core-backed service metadata

Management API публикует Core-owned resources:

```text
POST /api/services/{instanceId}/replicas/register
GET  /api/services/{instanceId}/manifest
GET  /api/services/{instanceId}/schema
GET  /api/services/{instanceId}/status
GET  /api/service-links/{callerInstanceId}/{targetInstanceId}
POST /api/service-links
PUT  /api/service-links/{callerInstanceId}/{targetInstanceId}
```

CLI отправляет в registration только безопасные runtime metadata. Core
проверяет live replica и сам вызывает Service SDK REST API для manifest,
configuration schema, identity и readiness. В CLI нет Service SDK client и
нет прямого service endpoint fallback.

Замена link policy использует Core revision в `If-Match` и отдельный
`Idempotency-Key`. Идентификаторы instance/replica, contract IDs и application
methods не переименовываются.

Для локального target CLI также использует ограниченный каталог секретных
ссылок Core:

```text
GET    /api/services/{serviceId}/secrets?purpose=<purpose>
POST   /api/services/{serviceId}/secrets
DELETE /api/services/{serviceId}/secrets/{referenceId}
```

В `POST` отправляются только opaque reference, назначение и label. Значение
вводится через скрытый терминальный prompt или отдельный secure stdin host-поток,
сохраняется в OS keyring и материализуется для Core с правами `0600` внутри
target-owned `secrets/`. Оно не попадает в Core request, CLI events, argv,
Project или отчёты. `service secret rotate` создаёт и регистрирует новую opaque
ссылку, а локальный CLI сохраняет значение и связь с предыдущей ссылкой как
`staged`. При недоступности Core регистрацию можно повторить командой `register`
без повторного ввода. Старая ссылка остаётся активной до явного подтверждённого
`revoke`. Studio не разрешает этот шаг, пока сохранённый Project не перестанет
использовать старую ссылку и все реплики не подтвердят точные digest и generation
новой конфигурации; при прямом вызове CLI эти условия проверяет оператор.
