# Contributing

## Development loop

```bash
make fmt
make check
make test-race
staticcheck ./...
```

GitHub CI дополнительно проверяет pinned Core Management contract, собирает
поддерживаемые платформы и выполняет local Core mTLS smoke lifecycle.

Каждый use case живёт в `internal/application`, модели и порты — в
`internal/domain`, внешние системы — в `internal/infrastructure`, а разбор
команд и exit codes — в `internal/presentation/cli`. Composition root находится
только в `cmd/liapoldus`.

Не добавляйте Core SQLite, Git provider API или process-specific policy в
application/domain. Новые публичные команды должны иметь machine-readable
output, typed exit behavior и тест успешного и отказного сценария.

Перед изменением Core API сначала обновляется owner contract в Core, затем
совместимый typed adapter и contract test в CLI.
