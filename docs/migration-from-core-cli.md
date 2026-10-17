# Миграция: CLI выходит из Core

## Решение

Переход выполняется сразу как breaking change. Поддерживаемого периода, в
котором старый Core CLI и новый универсальный CLI реализуют одну и ту же
поверхность, нет.

## Что переезжает в `Liapoldus/cli`

- project/revision resolution;
- validation, plan и bundle building;
- target management;
- Core API client;
- apply/operation observation;
- local Core process lifecycle;
- поддержанные remote execution adapters;
- GitHub CI integration и deploy reports;
- пользовательская документация команд.

## Что остаётся в Core

- Core runtime;
- Management API;
- SQLite migrations и repositories;
- desired/active/previous/staging generation model;
- operations, leases, replicas, readiness и audit;
- service-neutral API schemas и generated clients/contracts.

## Что удаляется из Core

- `cmd/core` как CLI composition root;
- `internal/presentation/cli`;
- Core CLI command contracts и help text;
- Core CLI-specific tests;
- project/Git/deploy code;
- старые docs routes под `/core/cli/`.

## Состояние breaking migration

Core-side CLI entrypoints, bootstrap dispatcher, direct SQLite migration command
и CLI asset удалены. В этом репозитории находится standalone revision/bundle
boundary: `project inspect`, `validate`, `bundle build`, `plan`, guarded `apply`,
target registry, local process adapter и typed Management API transport. Эти
функции не возвращаются в Core.

Нельзя считать миграцию завершённой, если старые Core CLI routes, команды или
параллельные lifecycle semantics остаются описанными как поддерживаемые.
