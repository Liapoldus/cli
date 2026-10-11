# Security policy

Не публикуйте в issues и logs токены, private keys, certificates, Core
settings или bundle reports с secret values. CLI исключает `.env*`, `*.key`,
`*.pem`, `*.secret` и `secrets/` из Git bundle. Desktop Core tokens хранятся
только в системном credential vault: macOS Keychain, Windows Credential
Manager или Linux Secret Service. Target profile содержит scoped reference,
но не token. Если platform vault недоступен, CLI закрывает операцию без
plaintext fallback.

`liapoldus credential set --target <name>` принимает token только через
скрытый TTY prompt; `--replace` требует явной замены. Studio получает только
безопасный статус и никогда не видит значение. Для одноразового CI вызова
разрешён `--token-stdin`; raw `--token` не принимается. При создании нового
local Core CLI генерирует случайный bootstrap token, сохраняет его в vault и
передаёт только при первичной инициализации. Повторный запуск credential не
ротирует.

О потенциальных уязвимостях сообщайте владельцам Liapoldus приватным каналом,
приложив минимальный воспроизводимый пример без секретных данных. Не создавайте
публичный issue до подтверждения remediation plan.
