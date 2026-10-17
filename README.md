# Liapoldus CLI

Универсальный CLI для сборки и доставки конфигураций Liapoldus Core.

CLI является отдельным продуктом и репозиторием. Он используется одинаково из
Liapoldus Studio, локального терминала и CI/CD. Core предоставляет runtime и
Management API, но не содержит собственного пользовательского CLI.

## Что делает CLI

- читает Studio Project из конкретной Git-ревизии;
- проверяет наличие project manifest и валидность service settings;
- собирает детерминированный config bundle;
- показывает plan и diff для выбранного Core target;
- применяет bundle к локальному или удалённому Core через API;
- управляет несколькими Core targets и Docker lifecycle локального Core;
- ожидает операции, возвращённые Core после apply;
- поддерживает HTTPS/mTLS connection profiles и remote approval evidence;
- работает в GitHub Actions и других CI без зависимости от Studio.

Executable v2 фиксирует обязательную границу revision: `plan`, `bundle build` и
`apply` разрешают только Git commit, а `apply` дополнительно требует чистое
дерево и явный `--confirm`. `plan` и `apply` отправляют bundle через Core
Management API; Core сам владеет SQLite. Bundle собирается детерминированно из
указанной отсортированной Git-ревизии и не включает secrets/`.studio` state.

Код разделён на domain, application, infrastructure и presentation слои.
`cmd/liapoldus` — единственный composition root; Core API, Git и Docker runtime
доступ не просачиваются в application policy. Локальный Core не запускается как
host-процесс.

## Что CLI не делает

- не становится вторым хранилищем исходных конфигов;
- не пишет файлы в Core и не монтирует project directory в Core;
- не обходит Git approval policy;
- не передаёт незакоммиченные изменения в `apply`;
- не содержит product-specific runtime logic вместо Core services;
- не исполняет StudioPlugin UI.

Подробная целевая спецификация находится в [`docs/`](docs/index.md).
