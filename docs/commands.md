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

## Локальная разработка из Studio

Для локальной итерации Studio использует отдельные команды. Они захватывают
текущие tracked/untracked project files в закрытый временный snapshot, исключая
`.studio/` и секретные пути. Git checkout не индексируется и не коммитится.
Request revision имеет вид `working-tree:{bundle-digest}` и применяется только к
target `local`:

```bash
printf '%s\n' "$CORE_TOKEN" | liapoldus dev plan \
  --project ./project --target local --endpoint https://127.0.0.1:18180 \
  --token-stdin

printf '%s\n' "$CORE_TOKEN" | liapoldus dev apply \
  --project ./project --target local --confirm --wait \
  --endpoint https://127.0.0.1:18180 --token-stdin
```

`dev apply` требует явный `--confirm`; remote targets и произвольный `--revision`
не поддерживаются. Capture завершается ошибкой при symlink/path escape, изменении
Project во время snapshot или превышении лимитов размера. Обычный `apply` остаётся
commit-based и не меняет этот контракт.

Studio `dev apply` сериализуется межпроцессной блокировкой на локальный Core
target. Перед отправкой CLI атомарно записывает recovery intent с Core scope и
opaque per-service idempotency keys; project settings, payload, credentials и
endpoint в journal не записываются. После потери процесса следующий apply
находит принятые операции через `POST /api/operations/resolve`, ждёт их terminal
state и только затем планирует новый draft. Terminal `failed` приостанавливает
очередь: сначала проверьте Core operation report, затем повторите подтверждённый
Studio apply с `--retry-failed`, чтобы явно разрешить новую попытку. Для
`degraded` и неизвестных состояний сохраняется fail-closed поведение. При
degraded rollout Core уже мог продвинуть desired configuration: сначала
сверьте состояние replicas или выполните явный rollback через Core; флаг retry
не обходит эту проверку.

`dev apply --wait` выводит safe summary: принятый Core response и, когда Core
вернул результаты реплик, массив `replicaOutcomes` с replica ID, generation,
состоянием и безопасным failure code. При частичном или degraded результате
outcomes остаются в stdout вместе с диагностикой и ненулевым exit code; CLI не
объявляет общий rollback. Проверяйте фактическое состояние через `operation
get|watch` перед повторным применением.

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
liapoldus core settings inspect --target local
liapoldus core binary inspect --path /path/to/core-linux-arm64
liapoldus core binary update --target local --path /path/to/core-linux-arm64 --digest sha256:<exact-digest> --confirm
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

`core settings inspect` читает desired/effective revisions и возвращает
`core-settings-view/v1`: listen/timeouts/limits и состояния TLS references без
raw Core JSON, абсолютных путей, credentials или содержимого сертификатов.
Известные mount roles показываются как target-scoped `target-file/v1/...` IDs;
неизвестные пути отображаются как `unmapped` и не попадают в renderer. Это
read-only операция.

`core settings update --target local --request-file <typed-patch> --confirm`
принимает только `core-settings-patch/v1`: expected desired revision и
allowlisted scalar fields (`management.listen`, body limit/timeouts,
`serviceControl.listen` и `publicURL`). Неизменённые TLS/secret references
сохраняются из текущего Core snapshot внутри CLI. Raw Core JSON, paths и
credentials не передаются из Studio. CLI проверяет локальный target, CAS
revision, отсутствие другой pending revision, bounded regular request file и
явное подтверждение; затем применяет Core `If-Match` и возвращает redacted
snapshot. Core restart не выполняется этой командой: результат явно отмечает
новую desired/effective revision и требует отдельного restart workflow, который
ещё должен быть связан с управляемой local operation.

Для HTTPS/mTLS можно передать `--ca-file`, `--client-cert`, `--client-key` и
`--server-name`; обычно эти значения хранятся в target registry, а не в каждом
вызове.

Если target содержит `"approval": "remote-required"`, `apply` обязан получить
JSON evidence с `repository`, exact `revision`, `target`, `approved: true`,
`source` и `evidenceId`. `--confirm` подтверждает только локальное намерение и
не заменяет remote approval.

`core start/stop/restart/status/logs` управляют Docker-контейнером
`liapoldus-core`; host-процесс Core не запускается. `core stop` останавливает
только Core, а `core restart` перезапускает только уже существующий и
принадлежащий выбранному target контейнер Core. Эти команды не останавливают
сервисы, не удаляют target network или runtime staging. Для остановки всего
стека используется `runtime stop`. Core и runtime services подключаются к одной
target network. Management API опубликован только на `127.0.0.1`, Service SDK
control/peer endpoints доступны только внутри network.

`core start` и `runtime start` сверяют SHA-256 настроенного Core-бинарника и
runtime image с метками контейнера выбранного target. При изменении CLI
пересоздаёт только принадлежащий этому target контейнер Core, сохраняя его
state directory, SQLite, credentials, сертификаты и контейнеры сервисов.
Контейнер без точной метки владения не изменяется: команда завершается с
ошибкой. JSON результата `core start` содержит `created` и `updated`.

`core binary inspect` принимает выбранный оператором Linux ELF и возвращает
только SHA-256, платформу и размер — путь не возвращается в Studio. `core
binary update` требует точного повторного digest и `--confirm`, проверяет
strict platform match, запускает candidate Core и сохраняет путь/digest в CLI
target registry только после готовности Management API. Если запуск не удался,
предыдущая запись target остаётся активной; предыдущий Core container уже может
быть остановлен, поэтому Studio покажет ошибку, а восстановление выполняется
`core start --target local`. SQLite migrations эта операция не откатывает.
Обновление меняет только Core container; сервисные контейнеры, state path,
credentials и сертификаты сохраняются. Если candidate запустился, но registry
не удалось обновить, CLI остановит и удалит только candidate Core container;
ошибка очистки будет явно включена в диагностику.

`runtime status` проверяет каждый контейнер target runtime через allowlisted
Docker inspect projection и возвращает фактические `state`/`health` вместе с
именем и образом. Наличие остановленного контейнера не считается признаком
работающего сервиса; environment, mounts и другие чувствительные поля inspect
в ответ не попадают.

Docker provider различает отсутствие клиента и недоступность daemon:
`provider_missing` означает, что executable `docker` не найден; если клиент
установлен, но Docker/OrbStack daemon или выбранный context недоступен, команда
возвращает `provider_unavailable`. В последнем случае сначала запустите Docker
provider в его приложении и повторите typed `runtime status`; не удаляйте
контейнеры и Core state для исправления ошибки соединения.

При первом `core start` local target CLI создаёт и сохраняет случайный
management token в системном vault. Он передаётся Core через одноразовый файл с
правами `0600` во временном каталоге внутри смонтированного Core state directory.
CLI не создаёт отдельный bind mount из системного temp-каталога, который Docker
VM может не видеть. Token не попадает в Docker environment, аргументы или
`docker inspect`. Core удаляет файл до открытия API и хранит verifier. Повторный
запуск не меняет активный verifier; остаточный bootstrap path очищается. Если
credential отсутствует, нужно восстановить исходный token или выбрать новый
state path; CLI никогда не удаляет существующее состояние.

## Дополнительные команды диагностики

```bash
liapoldus operation get <operation-id> --target production
liapoldus operation watch <operation-id> --target production
liapoldus status --target production
liapoldus bundle build --project ./project --revision <commit> --target production
liapoldus bundle inspect <bundle-file>
liapoldus doctor --project ./project --revision <commit> --target production
```

`operation get|watch` возвращают только типизированную сводку операции:

```json
{
  "operationId": "op-…",
  "target": "production",
  "state": "degraded",
  "replicaOutcomes": [
    {
      "replicaId": "server-1",
      "desiredGeneration": 4,
      "state": "failed",
      "failureCode": "replica_unavailable"
    }
  ]
}
```

`replicaOutcomes` может отсутствовать, если Core ещё не сообщил результаты.
CLI проверяет схему и согласованность ответа с запрошенной операцией; неизвестные
поля Core отклоняются и не перенаправляются Studio.

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

Responses consumed by Studio use explicit allowlisted JSON DTOs rather than
serializing CLI registry/domain models. `target list|inspect|add` expose only
target identity, transport, approval and a runtime-capability flag;
`credential status` exposes target and state; `service artifact list|inspect|
register|trust` expose artifact identity, digest and trust state without local
binary paths. `runtime plan` omits the project root and artifact paths. Docker
`runtime inspect` returns only container name, image, state, health and published
ports; environment values, mount sources and raw Docker inspect JSON are never
forwarded to Studio.

## Binary-only service runtime

Studio и CLI не собирают service binaries и Docker images. Binary регистрируется
во внешнем CLI-owned registry, а Project хранит только logical artifact
reference в `services/{id}/service.yaml`. Версия, платформа и digest должны
совпасть с одной записью registry побайтно; CLI не выбирает «последнюю» версию
по ID и не подменяет отсутствующую ссылку.

```yaml
id: server
version: 1.1.0
artifact:
  id: server
  version: 1.1.0
  platform: linux/arm64
  digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
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
liapoldus service artifact register --id server --version 1.0.0 \
  --platform linux/arm64 --path /opt/liapoldus/bin/server
liapoldus service artifact trust --id server --version 1.0.0 \
  --platform linux/arm64 --digest sha256:...
liapoldus service artifact list
liapoldus runtime start --project ./project --revision HEAD --target local
```

`runtime start` поднимает Core и все выбранные готовые service binaries в Docker;
используется только заранее установленный runtime image. `docker build` и
Docker Compose для этого workflow не вызываются. Бинарники копируются в
OS-cache staging directory на срок жизни runtime и удаляются при `runtime stop`.
Внутренние service REST endpoints не публикуются на host; наружу открывается
только настроенный public endpoint Server на loopback.

`runtime start` сверяет контейнеры, помеченные точным runtime label выбранного
target. Если ожидаемые service containers остались в состояниях `created`,
`exited` или `dead`, CLI удаляет только эти остановленные ephemeral containers
перед запуском актуальных артефактов. Core, volumes, credentials, images,
staging binaries и контейнеры других targets не удаляются. Если service ещё
работает, найден неизвестный контейнер или Docker не смог однозначно проверить
состояние, Start завершается `runtime_exists` без очистки; проверьте `runtime
status` и остановите только этот target явной командой `runtime stop`.

Stack-level `runtime stop` останавливает и удаляет target-owned ephemeral
containers и binary staging. Studio перед этой операцией показывает отдельное
подтверждение с именем target и точным описанием удаляемого состояния; ручные
CLI-вызовы должны выполняться оператором только после проверки target.

Studio запускает отдельный контейнер через `runtime start|stop|restart --service
{service-id}`. Эти typed operations проверяют, что точное имя контейнера найдено
с runtime label выбранного target; произвольное Docker-имя не принимается.
`start` для остановленного сервиса сверяет live artifact version/digest из Core
с точным Project pin и CLI registry. При drift CLI заранее проверяет оба
trusted binary и staged digest, заменяет только остановленный target-owned
service container, регистрирует новый digest через Core и проверяет live Core
provenance + readiness. Если запуск, регистрация или проверка не проходит,
CLI восстанавливает прежний trusted binary и проверяет его Core provenance;
если rollback не подтверждён, возвращает отдельную actionable ошибку. При
отсутствии прежнего trusted binary замена не начинается. Запущенный контейнер
не заменяется: сначала Studio должен остановить только выбранный сервис.
`start` и `restart` завершаются успешно только после проверки Docker state и
Core readiness всех активных replicas. При падении процесса или тайм-ауте
readiness команда возвращает ошибку и ограниченный, redacted runtime output
вместо ложного `runtime.completed`.
Команды без `--service` управляют всем runtime.

После запуска service metadata читается только Core. Команды ниже являются
Core-backed Management API reads и не обращаются к Service SDK REST напрямую:

```bash
liapoldus service inspect server --target local
liapoldus service manifest server --target local
liapoldus service schema server --target local
liapoldus service status server --target local
liapoldus service admin-surface inspect server --target local
liapoldus service resource list server --resource sites --target local
liapoldus service resource publish server --project ./my-project \
  --page sites --action publish --artifact-file resources/site.tar.gz \
  --content-type application/gzip --metadata-file resources/publish.json \
  --if-match '"surface-digest"' --idempotency-key publish-site-001 --target local
```

`service resource list` принимает только catalog ID из live manifest сервиса.
CLI получает manifest и admin-surface digest через Core Management API,
отправляет bounded query обратно через Core и возвращает только поля
`value/label`, указанные в Service SDK catalog. CLI не подключается к Service
SDK REST и не выводит полный admin response.

`service admin-surface inspect` возвращает ограниченный до 1 MiB live
service-owned descriptor и его digest из Core Management API. Studio использует
его для отрисовки объявленных action forms; CLI не интерпретирует product
payload schema и не вызывает сервис напрямую.

`service resource publish` — отдельный explicit workflow для объявленного
service artifact action. `--if-match` передаётся только для action с CAS
revision; флаг optional. Artifact и JSON metadata должны быть обычными файлами
внутри указанного Project; symlink/path escape отклоняется. CLI получает live
Admin Surface и его digest только от Core, проверяет action/MIME/лимиты и
потоково отправляет multipart request обратно через Core Management API. Core
остаётся единственным компонентом, который обращается к Service SDK REST.
Успешный `202` возвращает operation reference; результат следует получать
через `operation get|watch`. Ни содержимое файлов, ни локальные пути не
включаются в operation/report output.

## Локальные секреты сервисов

Для `local` target CLI предоставляет typed каталог и OS-vault lifecycle:

```bash
liapoldus service secret list --target local --service server
liapoldus service secret create --target local --service server \
  --purpose http.auth.bearer --label "edge token" --confirm
liapoldus service secret rotate --target local --service server \
  --reference ssr:v1:<reference> --purpose http.auth.bearer \
  --label "edge token 2026-10" --confirm
liapoldus service secret register --target local --service server \
  --reference ssr:v1:<reference> --confirm
liapoldus service secret revoke --target local --service server \
  --reference ssr:v1:<reference> --confirm
```

`create` и `rotate` принимают значение через скрытый prompt. Studio использует
host-owned secure stdin; secret value не помещается в argv, stdout/stderr,
Project или Core metadata. `rotate` сохраняет новую ссылку и значение в
локальном OS vault, регистрирует только opaque metadata в Core и возвращает
состояние `staged`. Если регистрация Core не удалась, `list` покажет pending
registration, а `register` повторит её без повторного ввода значения.

Безопасный порядок ротации: создать новую ссылку, заменить ссылку в Project,
проверить redacted diff и подтвердить публичные/security-sensitive изменения,
применить конфигурацию, дождаться готовности всех реплик и подтверждения ими
точного digest/generation, затем отозвать старую ссылку. Studio проверяет
сохранённый Project и acknowledgements до вызова `revoke`. При прямом вызове CLI
оператор обязан вручную убедиться, что Project уже не ссылается на старую
ссылку и все реплики подтвердили новую конфигурацию. Отзыв требует `--confirm`,
блокирует дальнейшее redemption в Core и очищает локальное staged-хранилище и
OS-vault запись. Автоматического планировщика ротации и remote secret backend
нет; secret operations поддерживаются только для `local`.
