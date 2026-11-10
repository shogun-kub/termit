# Как запустить сервер управления (termit-mn)

[English](../using-mn.md) · [Русский](using-mn.md)

Сервер управления — та самая машина, которая держит сеть в порядке: он хранит список участников,
проверяет каждый узел снаружи и отдаёт каждому владельцу подписку. Ваш трафик через него не идёт.

Нужно: Debian 13 (2 vCPU, 2 ГБ RAM, 10 ГБ диска), root, DNS-имя, указывающее на машину, и свободные
порты 80 и 443. Одного сервера хватает на группу до нескольких десятков узлов; кластера нет и не
нужно — если сервер упадёт, установленные соединения продолжат работать.

Если проще выбрать из списка, чем вспоминать флаги, запустите `termit-mn` без аргументов на сервере — это
[интерактивное меню](menu.md).

## Установка

```bash
install -m 0755 termit-mn /usr/bin/termit-mn

termit-mn init --domain mn.example.com --email you@example.com --mode invite
```

`--mode` решает, как узел вступает:

| Режим | Что происходит при вступлении узла |
|---|---|
| `invite` | кода приглашения достаточно |
| `invite+approval` (по умолчанию) | узел ждёт как `pending`, пока вы не выполните `termit-mn uv approve` |
| `open` | приглашение не нужно — разумно только в приватной сети |

`termit-mn init` создаёт конфигурацию, базу, мастер-ключ и первый админ-токен и печатает секреты **один
раз**:

```
domain:       mn.example.com
listen:       :443
api prefix:   /6rfjdqfn5nno
masking site: portal (what a prober gets for every path the API does not claim)
config:       /etc/termit/mn/config.yaml
database:     /var/lib/termit/mn/termit.db (schema 5)
master key:   /etc/termit/mn/master.key (generated now)

admin token (shown once, store it now):
  <token>

first invite code (shown once, 1 use(s)):
  ABCD-EFGH-IJKL
```

Три вещи, на которые стоит посмотреть. **Префикс API** — это путь, по которому живёт API узлов:
`init` выбирает случайный, поэтому сканер, пробующий `/api/v1`, на вашем сервере не находит ничего, а
каждый узел получает полный URL — `https://mn.example.com/6rfjdqfn5nno`, — а не голый домен. Это не
секрет, и [справочник по API](../../MN-API.md) описывает, что за ним. `--api-prefix /api/v1` оставляет
старый, обнаруживаемый путь, если он нужен.

**Маскировочный сайт** — это то, что видит посетитель: обычная на вид страница на `/`, собственная 404
для всего остального и ни слова про Termit. Хост, который на любой путь отвечает `404 page not found`,
сам по себе отпечаток. `--decoy-template product` выбирает другой архетип (на узлах те же два), позже
это делает `decoy.template` в конфигурации. Страница генерируется при старте и живёт в памяти — на
диске нет ничего, что можно было бы отредактировать.

Две вещи нужно сохранить, и сохранить вместе: **админ-токен** (без него не работает ни одна команда
`mn`) и **мастер-ключ** (`/etc/termit/mn/master.key`). Без мастер-ключа база не сможет вывести ни
одну парную пару — это не пароль, который можно сбросить, это соль всего реестра. Храните их с
резервной копией базы, но не рядом с ней.

Положите токен в окружение, чтобы он не попал в историю шелла:

```bash
export TERMIT_MN_ADMIN_TOKEN='<token>'
```

Или храните его в файле — это то, что нужно таймеру, cron-задаче или Ansible: у них нет шелла, из
которого можно экспортировать переменную. CLI читает файл только когда его об этом просят, и
отказывается от файла, за который не может поручиться:

```bash
# записать один раз, и так, чтобы никто другой не прочитал
install -m 0600 /dev/null /etc/termit/mn/admin.token
printf '%s\n' '<token>' > /etc/termit/mn/admin.token

termit-mn status --token-file /etc/termit/mn/admin.token
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token   # или указать один раз в шелле
```

Файл должен быть с правами `0600` и принадлежать тому пользователю, который запускает команду; всё
остальное отклоняется с той самой командой `chmod`, которая это исправляет. Файл, читаемый группой или
всеми, — это утёкшие учётные данные, которые выглядят как аккуратная настройка.

`termit-mn init --admin-token-out FILE` записывает токен сразу в файл с правами `0600`, а не печатает
его, — чтобы у подготовленного сервера единственная копия не жила в чьей-то прокрутке терминала:

```bash
termit-mn init --domain mn.example.com --email you@example.com \
  --admin-token-out /etc/termit/mn/admin.token
```

Если запись не удалась, `init` всё равно напечатает токен и завершится с ошибкой: к этому моменту токен
уже есть в базе, а токен, которого никто не видел, оставил бы сервер без администратора.

Если токен утёк — обновите его. Команда заменяет тот креденшел, которым она аутентифицировалась (свой,
а не чужой), и один раз отдаёт новый:

```bash
termit-mn admin rotate --token-out /etc/termit/mn/admin.token
```

Каждый скрипт, таймер и playbook, в котором ещё лежит старый токен, перестаёт работать в этот же
момент. Ради этого обновление и делают, и это же стоит проверить перед запуском. Для обновления нужен
текущий токен: если потерян именно он, порядок восстановления — в
[руководстве по эксплуатации](../../OPERATIONS.md#the-admin-token-is-lost).

## Первый запуск

```bash
install -m 0644 termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
journalctl -u termit-mn -n 30 --no-pager
```

Первый запуск **выпускает сертификат до того, как начать отдавать TLS**, и отказывается стартовать,
если выпустить не удалось: API управления, тихо откатившийся на HTTP, хуже, чем неподнявшийся.
Ожидайте минуту-две, затем:

```
level=INFO msg="issued a certificate" domain=mn.example.com not_after=…
level=INFO msg="serving TLS" domain=mn.example.com listen=:443 not_after=…
level=INFO msg="admin socket ready" path=/run/termit/mn/admin.sock
```

Проверка:

```bash
curl -sS https://mn.example.com/healthz        # {"status":"ok", …}
termit-mn status
```

`termit-mn status` — экран, который открывают первым, каждый раз:

```
server:   mn.example.com
listen:   :443
api:      /6rfjdqfn5nno
decoy:    portal
version:  termit-mn v0.1.0 (commit …, built …, api 1, profile v1)
uptime:   3h12m (started 2026-11-10 11:18)
database: /var/lib/termit/mn/termit.db (412.0 KiB, schema 5)
prober:   ready (/usr/lib/termit/bin/xray)
queue:    0 due, next 2026-11-11 12:34 UTC
cert:     2027-02-08T12:06:44Z (in 89 days)
nodes:    3 (3 active)
check:    every 24h, times below are UTC

DOMAIN             STATE   FAILED  LAST CHECK         NEXT CHECK         LAST SEEN
node1.example.com  active  0       2026-11-10 13:29   2026-11-11 12:34   2026-11-10 13:31
```

Две строки ловят сеть, которая выглядит здоровой, но не работает: `prober: unavailable` значит, что
ни одна проверка не может дать вердикт (все узлы остаются как были, ничего не блокируется), а растущее
`queue: N due` — что планировщик не успевает. Ни то, ни другое не является отказом узла.

Сервер продлевает свой сертификат сам: выпускает при старте, перепроверяет каждые шесть часов и
подменяет файл без перезапуска. Строка `cert:` показывает, что он реально отдаёт.

## Приглашение узлов

```bash
termit-mn invite create --note "узел Ани" --uses 1
termit-mn invite create --uses 5 --expires-hours 48      # партия с дедлайном
termit-mn invite list
termit-mn invite revoke ABCD-EFGH-IJKL
```

Код приглашения — секрет с ограниченным числом использований; передавайте его по каналу, которому
доверяете, и отзывайте, если он утёк. Каждое использование расходует одно место.

## Наблюдение и управление узлами

| Задача | Команда |
|---|---|
| список узлов | `termit-mn uv list` |
| посмотреть один узел | `termit-mn uv show node1.example.com` — состояние, история, последняя проверка и самопроверка |
| одобрить ожидающий узел | `termit-mn uv approve node1.example.com` |
| отклонить ожидающий узел | `termit-mn uv reject node1.example.com` |
| заблокировать узел самому | `termit-mn uv block node1.example.com --reason "жалоба"` |
| снять свою блокировку | `termit-mn uv unblock node1.example.com` |
| убрать узел из сети | `termit-mn uv rm node1.example.com` |
| стереть запись | `termit-mn uv forget node1.example.com` (только после удаления) |
| прочитать ключ агента | `termit-mn uv agentkey node1.example.com` |
| заменить ключ агента | `termit-mn uv reissue node1.example.com` |
| проверить сейчас | `termit-mn check now node1.example.com` или `termit-mn check now --all` |
| поправить расписание | `termit-mn policy show`, `termit-mn policy set check_interval 12h` |
| обновить токен подписки | `termit-mn sub rotate node1.example.com --domain mn.example.com` |

Что означают состояния:

| Состояние | Значение | Чем снимается |
|---|---|---|
| `pending` | вступил, но ещё не одобрен (`invite+approval`) | `termit-mn uv approve` |
| `active` | проверен и работает | — |
| `unavailable` | проверка прошла и честно провалилась (сертификат, приманка, туннель) | успешными проверками |
| `blocked` | узел заблокирован оператором | `termit-mn uv unblock` или запрос самого узла `termit-uv unblock` |
| `removed` | вне сети; учётные данные перестают работать | повторным вступлением (новая запись) |

Состояние меняется только по вердикту. Проверка, которая не смогла выполниться — prober не смог отработать, DNS
на сервере сломан, таймаут — это `unknown`, и она ничего не меняет. Поэтому узел может выглядеть
«зависшим» в `termit-mn uv show` и при этом быть в порядке.

`block` и `unblock` — ваше слово против слова проверки: успешная проверка не снимает вашу блокировку,
и именно это не даёт «мигающей» проверке мигать всей сетью.

## Подписки

У каждого узла один токен. Эндпоинты:

| URL | Содержимое |
|---|---|
| `https://mn.example.com/sub/<token>` | base64 из одного `vless://` на строку — тот самый «URL подписки», который нужен клиенту |
| `https://mn.example.com/sub/<token>/json` | те же пиры как профиль sing-box, с селектором и `direct` для приватных диапазонов |

Оба стабильны при неизменном составе пиров и несут `ETag`, поэтому кэширующий клиент стоит одного
`304`. Свой URL узел показывает в `termit-uv status`. Обновление токена мгновенно отменяет старый URL и не
затрагивает локальные устройства узла — они с машины никуда не уходят.

## Резервные копии и восстановление

```bash
termit-mn backup                                        # <data_dir>/backups/termit-<timestamp>.tar.gz
termit-mn backup --out /root/mn-backup.tar.gz
```

В архиве база и мастер-ключ. Держите его зашифрованным и храните копию вне сервера: у кого оба, у того
все парные учётные данные сети.

```bash
termit-mn restore --from /root/mn-backup.tar.gz --force  # сервер сначала остановить
```

Восстановление — офлайн-операция: остановить `termit-mn`, восстановить, запустить. Восстановленная база
полезна только с тем же мастер-ключом — если база есть, а ключа нет, узлам придётся вступать заново.

## Мониторинг

`/metrics` доступен только на loopback, в формате Prometheus, и никогда не называет узел:

```
termit_mn_up 1
termit_mn_nodes{state="active"} 3
termit_mn_check_queue_due 0
termit_mn_prober_up 1
termit_mn_checks_total{result="ok"} 42
termit_mn_certificate_expiry_timestamp_seconds 1801439850
```

На что ставить алерты: пропал `up`, `certificate_expiry_timestamp_seconds` меньше недели,
растёт `check_queue_due`, `prober_up 0`, растёт `checks_total{result="failed"}`. Детали по узлам
остаются в `termit-mn uv show` и логах — у endpoint'а намеренно нет инвентаря.

Логи идут в journal, структурированные, с идентификаторами узлов и без учётных данных:

```bash
journalctl -u termit-mn -f
```

## Справочник команд

| Команда | Что делает |
|---|---|
| `termit-mn init` | создать конфигурацию, базу, мастер-ключ, админ-токен и первое приглашение; `--admin-token-out FILE` пишет токен в файл вместо печати |
| `termit-mn serve` | запустить сервер (`--http` только для разработки, никогда на публичном адресе) |
| `termit-mn invite create\|list\|revoke` | управление кодами приглашений |
| `termit-mn uv list\|show\|approve\|reject\|block\|unblock\|rm\|forget\|reissue\|agentkey` | управление узлами |
| `termit-mn check now <узел>\|--all` | выполнить проверки немедленно |
| `termit-mn policy show\|set` | посмотреть или изменить пороги планировщика |
| `termit-mn sub rotate <узел>` | выпустить новый токен подписки |
| `termit-mn admin rotate` | заменить админ-токен, которым команда аутентифицировалась; `--token-out FILE` пишет его в файл |
| `termit-mn status` | сервер, узлы, расписание |
| `termit-mn backup`, `termit-mn restore` | снимок и восстановление |
| `termit-mn version` | версия сборки и протокола |

Админ-команды общаются с работающим сервером через unix-сокет (`/run/termit/mn/admin.sock`) и
аутентифицируются `TERMIT_MN_ADMIN_TOKEN`, `TERMIT_MN_ADMIN_TOKEN_FILE`, `--token` или `--token-file`. Они никогда не открывают базу в обход сервера.

## Правила, которые стоит соблюдать

- **Никогда не правьте базу руками.** Это реестр; интерфейс — админ-команды.
- **Храните мастер-ключ вместе с базой, и оба — зашифрованными.** Без ключа ничего не восстановить.
- **Запускайте один сервер.** Два экземпляра на одной базе её испортят, и второй всё равно не сможет
  выпустить сертификат на то же имя.
- **Держите строку `prober` в `termit-mn status` зелёной.** Мёртвый prober — не сломанная сеть, но слепая.
- **Берегите админ-токен как мастер-ключ.** Он создаётся в `termit-mn init` и хранится хешем; `termit-mn
  admin rotate` заменяет его, и старый перестаёт работать сразу. Держите его в файле с правами `0600`, а
  не в истории шелла, и не включайте в копии, которыми делитесь.

Дальше: [Как пользоваться узлом](using-uv.md) · [Если что-то пошло не так](troubleshooting.md)
