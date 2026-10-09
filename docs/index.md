# Liapoldus CLI

## Статус документа

Это нормативная спецификация отдельного универсального CLI. Репозиторий владеет
CLI-кодом, command contract и deployment workflow; Core не содержит его копии.

## Главная идея

Studio Project и Git — источник исходных конфигураций. CLI превращает выбранный
commit в детерминированный bundle и передаёт его Core API. Core сохраняет
применённую конфигурацию в SQLite и управляет runtime rollout.

```text
Studio Project files + Git commit
             │
             ▼
      liapoldus CLI
  resolve → validate → plan
             │
             ▼
       Core Management API
             │
             ▼
      Core-owned SQLite writes
             │
             ▼
       replicas / operations
```

## Документы

| Раздел | Содержание |
| --- | --- |
| [Архитектура и границы](architecture) | ownership, source of truth и компоненты |
| [Project и config bundle](project-and-bundle) | файлы проекта, материализация и SQLite |
| [Команды](commands) | целевая command surface |
| [Core API contract](core-api) | transport, idempotency, CAS и operation model |
| [Targets и несколько Core](targets) | local, remote, target profiles и lifecycle |
| [GitHub CI и approval](ci-and-approval) | commit-gated deploy и remote governance |
| [Миграция Core CLI](migration-from-core-cli) | одномоментное удаление CLI из Core |
| [Roadmap](roadmap) | vertical slices, acceptance criteria и открытые решения |

## Нормативные инварианты

1. Локальное Git-версионирование проекта всегда включено.
2. `apply` принимает только существующий commit или другой immutable revision.
3. Studio не вызывает Core API и не выполняет deploy.
4. Один и тот же CLI используется локально и в CI.
5. Core SQLite — источник истины применённого runtime desired state.
6. Git не заменяет SQLite, а SQLite не заменяет Git.
7. Remote approval policy проверяется до отправки bundle и дополнительно
   enforce-ится самим Git remote/CI.
