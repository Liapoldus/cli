# Security policy

Не публикуйте в issues и logs токены, private keys, certificates, Core
settings или bundle reports с secret values. CLI исключает `.env*`, `*.key`,
`*.pem`, `*.secret` и `secrets/` из Git bundle; credentials передаются только
через environment/CI secret provider, target reference или `--token-stdin`.
Не передавайте production token через `--token`: значение может попасть в
shell history или process list.

О потенциальных уязвимостях сообщайте владельцам Liapoldus приватным каналом,
приложив минимальный воспроизводимый пример без секретных данных. Не создавайте
публичный issue до подтверждения remediation plan.
