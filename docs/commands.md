# Целевая command surface

Команды ниже описывают ownership и UX. Имена и flags должны стать code-owned
контрактом после появления первой реализации.

## Project и validation

```bash
liapoldus project inspect --project ./project
liapoldus validate --project ./project --revision <commit> --environment production
liapoldus plan --project ./project --revision <commit> --target production
```

`validate` не требует Core. `plan` может использовать только read-only Core API
для сравнения target state, но не меняет Core.

## Apply и observations

```bash
liapoldus apply \
  --project ./project \
  --revision <commit> \
  --target production \
  --wait

liapoldus operation get <operation-id> --target production
liapoldus operation watch <operation-id> --target production
liapoldus status --target production
```

`apply` требует resolved immutable commit, approval policy, target capability,
валидный bundle и idempotency key. При отказе Core или degraded rollout CLI
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
```

Target profile не должен содержать private key или secret value. Credentials
выбираются по reference из OS credential store, agent или CI secret provider.

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
