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

Для ручного ввода token без попадания в shell history/process arguments можно
использовать `--token-stdin`; CLI читает максимум 4 KiB и удаляет окружающие
пробелы. В CI предпочтителен target `tokenEnv` через credential provider.

## Targets и connections

```bash
liapoldus target list
liapoldus target inspect production
liapoldus target add production --profile ./environments/production.json
liapoldus target test production
liapoldus core status --target local
liapoldus core start --target local
liapoldus core stop --target local
liapoldus core logs --target local
liapoldus core database backup --target local --output ./backup.sqlite
liapoldus core database restore --target local --input ./backup.sqlite
```

Target profile — JSON без private key или secret value. Credentials выбираются
по reference из environment/CI secret provider; интеграция с OS credential
store остаётся production-ready задачей.

Для HTTPS/mTLS можно передать `--ca-file`, `--client-cert`, `--client-key` и
`--server-name`; обычно эти значения хранятся в target registry, а не в каждом
вызове.

Если target содержит `"approval": "remote-required"`, `apply` обязан получить
JSON evidence с `repository`, exact `revision`, `target`, `approved: true`,
`source` и `evidenceId`. `--confirm` подтверждает только локальное намерение и
не заменяет remote approval.

При `core start` local target adapter передаёт Core `CORE_SQLITE_PATH` и
одноразовые secure bootstrap references; Core сам создаёт initial settings
revision. Для remote target CLI не получает доступ к SQLite и использует
API/execution adapter.

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
