# Project и config bundle

## 1. Project source

CLI работает с тем же Project, который открывает Studio. Минимальная целевая
структура:

```text
project/
├── project.yaml                 # identity, format и target profiles
├── services/
│   └── <service-id>/
│       ├── service.yaml         # binding и schema reference
│       ├── settings/            # declarative settings source
│       └── links/               # Core-owned link declarations
├── modules/                     # project files для Studio plugins
├── schemas/                     # references или project-owned schemas
├── environments/
│   ├── local.yaml
│   └── production.yaml
└── .studio/                     # local generated state, не source
```

Точная схема файлов принадлежит отдельному project contract. CLI не должен
создавать параллельный формат только для CI.

## 2. Revision requirement

Команды `plan` и `apply` принимают commit SHA, tag или branch только как
удобный способ разрешить immutable revision. Перед materialization CLI обязан:

1. проверить наличие revision в локальном Git checkout;
2. получить resolved commit SHA;
3. убедиться, что working tree не содержит изменений, влияющих на bundle;
4. зафиксировать SHA в report и request;
5. отказать в `apply`, если revision нельзя однозначно воспроизвести.

`apply` никогда не читает draft из незакоммиченного working tree.

## 3. Materialization pipeline

```mermaid
sequenceDiagram
    actor Operator
    participant CLI
    participant Git
    participant Project
    participant Core
    participant SQLite

    Operator->>CLI: apply --project P --revision SHA --target production
    CLI->>Git: resolve and verify SHA
    Git-->>CLI: immutable commit
    CLI->>Project: load manifest + environment
    Project-->>CLI: source documents and references
    CLI->>CLI: validate, resolve, normalize, hash
    CLI->>Core: apply(bundle, SHA, schema, idempotency key)
    Core->>Core: validate generic envelope and service schemas
    Core->>SQLite: one transaction: staging + desired metadata
    SQLite-->>Core: committed generation
    Core-->>CLI: operationId, generation, digest
    CLI->>Core: observe operation and rollout
    Core-->>CLI: succeeded / failed / degraded
```

## 4. Что именно попадает в SQLite

В Core API передаётся не набор файлов, а canonical config bundle:

- project identity;
- target/environment identity;
- source commit SHA и repository identity;
- bundle schema version;
- deterministic bundle digest;
- service instances и их opaque settings documents;
- Core-owned links;
- safe provenance metadata;
- references на secrets, но не значения secrets.

Core принимает bundle и в одной транзакции записывает desired configuration и
provenance metadata. Для каждой service configuration сохраняются raw JSON bytes
в соответствии с Core storage invariants; Core не переписывает
product-specific fields.

После транзакции Core сам управляет `staging → active`, `previous`, generation,
operation journal и replica acknowledgements. CLI не открывает Core SQLite и не
пишет desired state напрямую; local lifecycle/backup/restore доступны только
отдельному target execution adapter.

## 5. Determinism

Одинаковые project files, environment inputs и commit должны давать одинаковые
canonical bundle bytes и digest. В bundle builder запрещены:

- текущие timestamps как значения конфигурации;
- порядок файлов файловой системы;
- machine-local absolute paths;
- случайные identifiers;
- чтение незаявленных ENV variables;
- включение secret values.

В report должны быть видны input revision, target, environment, schema versions
и digest, чтобы plan можно было повторить в CI.

## 6. Secrets и внешние inputs

Project хранит только symbolic secret references. CLI получает значения из
локального secure provider или CI secret store, если это требуется Core API
протоколом, и передаёт их только по защищённому transport либо выполняет
server-side redemption. Значения не попадают в bundle digest display, logs,
Git, Core settings JSON или operation report.

## 7. Reproducibility и rollback

Rollback — это повторный `apply` уже известной Git revision либо специальная
Core rollback operation, если она разрешена API contract. CLI не делает rollback
по файловому snapshot и не извлекает конфигурацию из SQLite обратно в Git.
