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
- plugin-neutral API schemas и generated clients/contracts.

## Что удаляется из Core

- `cmd/core` как CLI composition root;
- `internal/presentation/cli`;
- Core CLI command contracts и help text;
- Core CLI-specific tests;
- project/Git/deploy code;
- старые docs routes под `/core/cli/`.

## Порядок одной breaking migration

1. Утвердить Core Management API bundle/apply contract.
2. Реализовать CLI transport client и contract tests against Core.
3. Перенести lifecycle adapters и локальные smoke workflows.
4. Перенести operator docs и CI workflows на `liapoldus`.
5. В одном изменении Core удалить CLI entrypoint, CLI adapters, assets, tests и
   документацию.
6. Обновить агрегатор docs и все ссылки на новый CLI.
7. Прогнать cross-repository acceptance: local, remote, multi-target, rejected
   approval, failed rollout и degraded rollout.

Нельзя считать миграцию завершённой, если старые Core CLI routes, команды или
параллельные lifecycle semantics остаются описанными как поддерживаемые.
