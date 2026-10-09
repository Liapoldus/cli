# Архитектура и границы

## 1. Компоненты

```mermaid
flowchart LR
    U["Developer / CI"] --> G["Git repository"]
    U --> CLI["liapoldus CLI"]
    G --> CLI
    CLI --> P["Project resolver"]
    P --> V["Validator + bundle builder"]
    V --> T["Target adapter"]
    T --> API["Core Management API"]
    T --> LC["Local Core process"]
    API --> DB["Core SQLite"]
    API --> R["Core replicas"]
    S["Liapoldus Studio"] --> G
    S -. "не вызывает" .-> API
```

### Studio

Studio владеет проектом на рабочей машине, file tree, structured editing и
полноценной Git-интеграцией: remotes, branches, commit, push, pull, merge,
rebase, cherry-pick, stash и conflict resolution. Studio не имеет Core
connection, Core credentials или Core API adapter.

### CLI

CLI владеет операционным workflow вокруг Core:

- выбор immutable revision;
- проверка и нормализация project source;
- target resolution;
- plan/diff;
- отправка bundle;
- polling operation и безопасный exit code;
- локальный Core lifecycle и поддержанные remote execution adapters.

CLI не является вторым редактором проекта. Он не изменяет source files и не
выполняет Git commit за пользователя.

### Core

Core владеет runtime control plane и SQLite:

- принимает versioned config bundle через Management API;
- валидирует generic envelope и service-owned schemas;
- атомарно сохраняет desired configuration;
- управляет active/previous/staging generations;
- запускает rollout к replicas и фиксирует operations, leases и readiness.

Core не содержит пользовательского CLI, Git client, Git credentials, project
resolver или deployment-provider integration.

## 2. Источники истины

| Данные | Источник истины |
| --- | --- |
| Исходные файлы и история | Git remote, если он подключён и принят источником истины; иначе local Git |
| Рабочие drafts | локальный working tree Studio |
| Нормализованный bundle | конкретный CLI run и его immutable digest |
| Применённый desired state | Core SQLite |
| Active generation и rollout | Core SQLite + Core runtime observations |
| Plugin/service binary provenance | deployment/operator layer, не CLI и не Core |
| Secrets | secure store/CI secret provider; не Git и не bundle body |

Remote Git может быть declared source of truth проекта. В этом режиме Studio и
CLI не должны считать локальную ветку deployable, пока revision не прошла
политику remote approval.

## 3. Принцип отсутствия прямого доступа Studio к Core

Studio может открыть проект offline и выполнить локальную проверку структуры.
Любая информация о Core, включая target status, replica state, operations и
apply result, появляется через отдельный запуск CLI или CI result, импортированный
как безопасный report. В текущем целевом продукте Studio не создаёт Core
connection и не вызывает Core API.

Это разделяет два цикла:

```text
Studio: edit → validate → commit → push/pull
CLI:    checkout revision → plan → approval check → apply → observe
```

## 4. Два режима CLI

### Declarative deploy mode

CLI работает с project path и revision, строит bundle и отправляет его через
Core API. Это основной режим для локального deploy и CI.

### Core lifecycle mode

CLI может запускать, останавливать, проверять и читать логи локального Core.
Для удалённого Core это разрешено только target adapter с явно объявленным
transport (`ssh`, managed agent или другой утверждённый execution provider).
Один лишь API endpoint не даёт права управлять удалённым процессом.

## 5. Удаление старой границы

После миграции в Core не остаются:

- `internal/presentation/cli`;
- `cmd/core` как пользовательский CLI entrypoint;
- CLI contracts и command tests;
- Git/deploy/project parsing code.

Core сохраняет единственный runtime composition root и Management API. CLI
собирается и релизится независимо.
