# Command surface

Команды ниже описывают текущую executable surface. Неизвестные команды и
отсутствие обязательных flags завершаются ненулевым кодом.

## Реализовано в текущем executable

```bash
liapoldus project inspect --project ./project
liapoldus validate --project ./project --revision <commit>
printf '%s\n' "$CORE_TOKEN" | liapoldus plan --project ./project --revision <commit> --target production \
  --expected-generation 41 \
  --endpoint https://core.example --token-stdin \
  --ca-file ./credentials/core-ca.pem \
  --client-cert ./credentials/cli.crt \
  --client-key ./credentials/cli.key
```

`validate` не требует Core. `plan` использует только read-only Core API и не
меняет Core.

## Apply и observations

```bash
printf '%s\n' "$CORE_TOKEN" | liapoldus apply \
  --project ./project \
  --revision <commit> \
  --target production \
  --expected-generation 41 \
  --endpoint https://core.example \
  --token-stdin \
  --ca-file ./credentials/core-ca.pem \
  --client-cert ./credentials/cli.crt \
  --client-key ./credentials/cli.key \
  --approval-evidence ./artifacts/approval.json \
  --wait
```

`apply` требует resolved immutable commit, чистое рабочее дерево, `--confirm`,
target endpoint/token и валидный bundle. При отказе Core CLI возвращает
ненулевой exit code. `--wait` ожидает операции, которые вернул Core в ответе на
apply.

Для ручного разового CI-вызова token можно передать через `--token-stdin`; CLI
читает максимум 4 KiB и удаляет окружающие пробелы. Для desktop target
используйте системный credential vault:

```bash
liapoldus credential status --target local
liapoldus credential set --target local       # скрытый интерактивный ввод
liapoldus credential set --target local --replace
liapoldus credential remove --target local --confirm
```

Raw `--token` и `tokenEnv` не поддерживаются. Studio может запросить только
безопасный status и никогда не получает token. В CI token остаётся ephemeral
secret и передаётся через stdin, а не сохраняется в desktop vault.

## Targets и connections

```bash
liapoldus target list
liapoldus target inspect production
liapoldus target add production --profile ./environments/production.json
liapoldus target test production
liapoldus observe traffic --project ./project --target local
liapoldus core status --target local
liapoldus core start --target local
liapoldus core stop --target local
liapoldus core logs --target local
liapoldus core database backup --target local --output ./backup.sqlite
liapoldus core database restore --target local --input ./backup.sqlite
```

Target profile — JSON без private key или secret value. Для report-based
observation target может дополнительно объявить `trafficReportPath` —
project-relative путь к CLI/Core report формата `studio-reports/v1`.
`observe traffic` только проверяет этот immutable report, его project/target и
размер, затем публикует его relative path и SHA-256 для Studio; он не
подключается к Core и не копирует raw payload в UI.

Target credential reference разрешается в системном credential vault. На
macOS, Windows и Linux используются соответственно Keychain, Credential
Manager и Secret Service; если platform vault недоступен, CLI не переходит к
plaintext file/environment fallback.

Для HTTPS/mTLS можно передать `--ca-file`, `--client-cert`, `--client-key` и
`--server-name`; обычно эти значения хранятся в target registry, а не в каждом
вызове.

Если target содержит `"approval": "remote-required"`, `apply` обязан получить
JSON evidence с `repository`, exact `revision`, `target`, `approved: true`,
`source` и `evidenceId`. `--confirm` подтверждает только локальное намерение и
не заменяет remote approval.

`core start/stop/restart/status/logs` управляют Docker-контейнером
`liapoldus-core`; host-процесс Core не запускается. Core и runtime plugins
подключаются к одной target network. Management API опубликован только на
`127.0.0.1`, Plugin SDK control/peer endpoints доступны только внутри network.

При первом `core start` local target CLI создаёт и сохраняет случайный
management token в системном vault. Он передаётся Core через одноразовый файл с
правами `0600` в приватном bind mount: token не попадает в Docker environment,
аргументы или `docker inspect`. Core удаляет файл до открытия API и хранит
только verifier. Повторный запуск не меняет активный verifier; остаточный
bootstrap path очищается. Если credential отсутствует, нужно восстановить
исходный token или выбрать новый state path; CLI никогда не удаляет существующее
состояние.

## Дополнительные команды диагностики

```bash
liapoldus operation get <operation-id> --target production
liapoldus operation watch <operation-id> --target production
liapoldus status --target production
liapoldus bundle build --project ./project --revision <commit> --target production
liapoldus bundle inspect <bundle-file>
liapoldus doctor --project ./project --revision <commit> --target production
```

Bundle file создаётся через `bundle build`; inspect и doctor выполняют только
локальную проверку bundle и доступности target. Bundle не является вторым source
tree и не должен автоматически коммититься в Git.

## Exit codes

Целевая production-ready версия должна различать как минимум:

| Код | Значение |
| --- | --- |
| `0` | операция успешно завершена |
| `2` | ошибка аргументов или project source |
| `3` | revision/approval policy не выполнена |
| `4` | schema или bundle validation failed |
| `5` | target/Core недоступен или authentication failed |
| `6` | Core operation failed |
| `7` | rollout degraded или не подтверждён всеми replicas |
| `8` | conflict, stale generation или idempotency mismatch |

Точные коды будут зафиксированы в generated CLI contract и не должны зависеть
от текста human-readable сообщения.

## Studio machine-readable stream

Desktop Studio запускает CLI с внутренним флагом `--studio-events`. Этот флаг
не является произвольным shell-режимом и не меняет ownership credentials/Core.
CLI emits one JSON object per line using `cli-events/v1`:

```json
{"schemaVersion":"cli-events/v1","type":"run.started","runId":"..."}
{"schemaVersion":"cli-events/v1","type":"run.output","runId":"...","payload":{}}
{"schemaVersion":"cli-events/v1","type":"diagnostic","runId":"...","code":"cli.stderr","message":"liapoldus emitted a non-JSON diagnostic"}
{"schemaVersion":"cli-events/v1","type":"run.completed","runId":"...","exitCode":0}
```

Plain command output is carried only as a JSON `payload`; human-readable stderr
is replaced with a generic diagnostic so target paths, credentials and internal
details do not cross the Studio boundary. The flag is intended for the typed
Studio adapter and is stripped before normal command dispatch.

## Binary-only plugin runtime

Studio и CLI не собирают plugin binaries и Docker images. Binary регистрируется
во внешнем CLI-owned registry, а Project хранит только logical artifact
reference в `services/<id>/service.yaml`. Версия, платформа и digest должны
совпасть с одной записью registry побайтно; CLI не выбирает «последнюю» версию
по ID и не подменяет отсутствующую ссылку.

```yaml
id: server
version: 1.1.0
artifact:
  id: server
  version: 1.1.0
  platform: linux/arm64
  digest: sha256:<64 lowercase hex characters>
runtime:
  command: server
  controlEndpoint: https://server:9443
  peerEndpoint: https://server:9643
```

Пути binary, trust state, credentials и Docker socket в Project не записываются.
Старый `service.yaml` без этой ссылки отклоняется; совместимого fallback нет.
Перед запуском можно получить разрешённый и проверенный plan:

```bash
liapoldus runtime plan --project ./project --revision HEAD --target local
```

Затем binary регистрируется во внешнем CLI-owned registry:

```bash
liapoldus plugin artifact register --id server --version 1.0.0 \
  --platform linux/arm64 --path /opt/liapoldus/bin/server
liapoldus plugin artifact trust --id server --version 1.0.0 \
  --platform linux/arm64 --digest sha256:...
liapoldus plugin artifact list
liapoldus runtime start --project ./project --revision HEAD --target local
```

`runtime start` поднимает Core и все выбранные готовые plugin binaries в Docker;
используется только заранее установленный runtime image. `docker build` и
Docker Compose для этого workflow не вызываются. Бинарники копируются в
OS-cache staging directory на срок жизни runtime и удаляются при `runtime stop`.
Внутренние plugin REST endpoints не публикуются на host; наружу открывается
только настроенный public endpoint Server на loopback.

После запуска plugin metadata читается только Core. Команды ниже являются
Core-backed Management API reads и не обращаются к Plugin SDK REST напрямую:

```bash
liapoldus plugin inspect server --target local
liapoldus plugin manifest server --target local
liapoldus plugin schema server --target local
liapoldus plugin status server --target local
```
