# Релиз и откат

## Публикация

Релиз создаётся только Git tag формата `vX.Y.Z`:

```bash
git tag -a v2.0.0 -m "liapoldus v2.0.0"
git push origin v2.0.0
```

GitHub Actions запускает GoReleaser и публикует архивы для Linux и macOS
(`amd64`, `arm64`), Windows (`amd64`) и `checksums.txt`. Версия бинаря
встраивается в `liapoldus version`; артефакты не зависят от Studio или Core
checkout.

Перед публикацией должны пройти quality, race, staticcheck, Core contract и
cross-build jobs. Конфигурация GoReleaser отдельно проверяется job
`release-config`.

## Установка и проверка

Распакуйте архив для целевой ОС и установите бинарь в каталог из `PATH`:

```bash
install -m 0755 liapoldus /usr/local/bin/liapoldus
liapoldus version
sha256sum -c checksums.txt --ignore-missing
```

На Windows проверка выполняется через `Get-FileHash`, после чего `liapoldus.exe`
добавляется в пользовательский `PATH`. Target registry и credentials не входят
в release archive и создаются отдельно на машине оператора или в CI.

## Откат

Откат выполняется заменой бинаря на предыдущий проверенный архив. Git revision
проекта и Core desired state при этом не изменяются. Повторный deploy выполняется
явной immutable revision и target:

```bash
liapoldus version
liapoldus plan --project ./project --revision <sha> --target production
```

Не откатывайте Core SQLite заменой файла во время работающего Core. Для local
target используйте `core stop`, `core database backup/restore`, затем повторный
`core start`; remote Core восстанавливается только его поддержанным operational
workflow.
