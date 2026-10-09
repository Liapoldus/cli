# Targets и несколько Core

## 1. Target model

Target — именованный deployment context проекта. Он связывает environment,
Core endpoint, transport capabilities, API compatibility и credential refs.

```yaml
targets:
  local:
    environment: local
    transport: local-process
    coreEndpoint: http://127.0.0.1:...
  production:
    environment: production
    transport: api
    coreEndpoint: https://core.example
    approval: remote-required
```

Такой YAML — концептуальная модель. Секреты, private keys и access tokens в
project source не хранятся.

## 2. Multiple Core workflow

Один Project может применяться к нескольким Core, но каждый `apply` явно
получает target. CLI не считает production продолжением local и не переносит
локальный SQLite.

```mermaid
flowchart LR
    C["One Git commit"] --> L["local target"]
    C --> S["staging target"]
    C --> P["production target"]
    L --> LDB["local Core SQLite"]
    S --> SDB["staging Core SQLite"]
    P --> PDB["production Core SQLite"]
```

Для каждого target отдельно фиксируются bundle digest, operation, generation,
replica observations и audit context.

## 3. Local Core

Local target может включать:

- бинарь Core;
- managed data directory;
- SQLite path;
- process state;
- local certificates/credentials через secure store;
- port and health endpoint.

Первый запуск local Core выполняет одноразовый bootstrap SQLite из
`CORE_INIT_*`. Эти значения приходят из secure target provider и не попадают в
project files, Git, bundle или logs.

`core start`, `core stop`, `core restart`, `core status` и `core logs` действуют
через target adapter. `core database backup/restore` использует поддержанный
Core API или execution adapter. CLI не изменяет SQLite напрямую и не использует
непубличные Core packages.

## 4. Remote Core

Remote target может поддерживать только API operations либо также lifecycle
operations через `ssh`, managed agent или другой зарегистрированный provider.
Эти возможности объявляются capability discovery и явно показываются в plan.

Удалённый API-only target разрешает `status`, `plan`, `apply` и observation, но
не обещает `start`/`stop`. Это предотвращает ложное ощущение, что Management API
владеет удалённым process supervisor.

## 5. Target isolation

- target selection обязательна для `plan` и `apply`;
- credentials одного target не доступны другому;
- operation polling не смешивает Core instances;
- bundle digest одинаков для одинакового environment input, но target metadata
  и operation IDs различаются;
- CLI никогда не копирует SQLite между targets.
