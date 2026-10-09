# Целевая command surface

Команды ниже описывают code-owned поверхность текущего v2 CLI. Неизвестные
команды и отсутствие обязательных flags завершаются ненулевым кодом.

## Project и validation

```bash
liapoldus project inspect --project ./project
liapoldus validate --project ./project --revision <commit> --environment production
liapoldus plan --project ./project --revision <commit> --target production \
  --endpoint https://core.example --token "$CORE_TOKEN"
```

`validate` не требует Core. `plan` использует только read-only Core API и не
меняет Core.

## Apply и observations

```bash
liapoldus apply \
  --project ./project \
  --revision <commit> \
  --target production \
  --endpoint https://core.example \
  --token "$CORE_TOKEN" \
  --wait

liapoldus operation get <operation-id> --target production
liapoldus operation watch <operation-id> --target production
liapoldus status --target production
```

`apply` требует resolved immutable commit, чистое рабочее дерево, `--confirm`,
target endpoint/token и валидный bundle. При отказе Core или degraded rollout CLI
возвращает ненулевой exit code и сохраняет machine-readable report.

## Targets и connections

```bash
liapoldus target list
liapoldus target inspect production
liapoldus target add production --profile ./environments/production.yaml
liapoldus target test production
liapoldus core status --target local
liapoldus core start --target local
liapoldus core stop --target local
liapoldus core logs --target local
liapoldus core database backup --target production --output ./backup.sqlite
liapoldus core database restore --target local --input ./backup.sqlite
```

Target profile не должен содержать private key или secret value. Credentials
выбираются по reference из OS credential store, agent или CI secret provider.

При `core start` local target adapter передаёт Core `CORE_SQLITE_PATH` и
одноразовые secure bootstrap references; Core сам создаёт initial settings
revision. Для remote target CLI не получает доступ к SQLite и использует
API/execution adapter.

## Bundle и диагностика

```bash
liapoldus bundle build --project ./project --revision <commit> --target production
liapoldus bundle inspect <bundle-file>
liapoldus doctor --project ./project --target production
```

Bundle file предназначен для диагностики и CI handoff. Он не является вторым
source tree и не должен автоматически коммититься в Git.

## Exit codes

CLI должен различать как минимум:

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
