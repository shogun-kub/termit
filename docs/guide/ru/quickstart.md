# Быстрый старт

Две машины и примерно пятнадцать минут: сервер, который хранит реестр, и узел, который несёт трафик.
Длинная версия — со всеми флагами и разбором отказов — в документах [Как устроен Termit](overview.md),
[Как запустить сервер](using-mn.md) и [Как пользоваться узлом](using-uv.md). Эта страница — короткий
путь до работающего телефона.

## Что нужно до начала

| | Сервер | Узел |
|---|---|---|
| Система | Debian 13, root по SSH | Debian 13, root по SSH |
| DNS | `mn.example.com` указывает на него | `node-a.example.com` указывает на него |
| Порты | 80 и 443 свободны и доступны снаружи | 80 и 443 свободны и доступны снаружи |
| Размер | 2 vCPU, 2 ГБ RAM | 1 vCPU, 1 ГБ RAM |

Узел присоединяется к серверу, а не наоборот — поэтому сервер идёт первым. Оба сертификата выдаются
Let's Encrypt прямо во время установки, так что DNS-записи должны уже резолвиться к моменту старта.

Свои имена подставляйте вместо `mn.example.com` и `node-a.example.com`.

## 1. Сервер

Скачайте релиз, сверьте его с контрольными суммами из него же и установите бинарник:

```bash
RELEASE=v0.4.0
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn_${RELEASE}_linux_amd64"
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 "termit-mn_${RELEASE}_linux_amd64" /usr/bin/termit-mn
```

Создайте конфигурацию, базу и первый администраторский токен. Токен сразу ложится в файл с правами
`0600`, чтобы единственная копия не осталась в истории терминала:

```bash
install -d -m 0750 /etc/termit/mn
termit-mn init --domain mn.example.com --email you@example.com \
  --admin-token-out /etc/termit/mn/admin.token
```

`init` напечатает **префикс API**, который выбрал сам, например `/6rfjdqfn5nno`. Запишите его: узлу
передаётся полный адрес `https://mn.example.com/6rfjdqfn5nno`, а не голый хост. Секретом префикс не
является — это случайный путь, на котором живёт API, чтобы сканер, постучавшийся в `/api/v1`, получил
маскировочный сайт.

Поставьте unit и запустите сервер:

```bash
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn.service"
install -m 0644 termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
```

Убедитесь, что он жив, и выпишите инвайт для узла:

```bash
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token
termit-mn status
termit-mn invite create --note node-a
```

Инвайт показывается один раз — в примерах это `ABCD-EFGH-IJKL`.

## 2. Узел, через мастер настройки

Бинарник ставится так же:

```bash
RELEASE=v0.4.0
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-uv_${RELEASE}_linux_amd64"
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 "termit-uv_${RELEASE}_linux_amd64" /usr/bin/termit-uv
```

Дальше пусть спрашивает сам:

```bash
termit-uv wizard
```

| Мастер спрашивает | Что ответить в этом примере |
|---|---|
| язык | `en` или `ru` — на нём будет всё дальнейшее |
| домен узла | `node-a.example.com` |
| адрес для ACME | `you@example.com` |
| какой маскировочный сайт | `portal` (или `product`) — что увидит посторонний на `https://node-a.example.com/` |
| WARP | `all` (всё проксируемое уходит через Cloudflare), `selected` или `off` |
| межсетевой экран и бан-движок | `yes`, если это только узел и ничего больше — см. [Защита узла](hardening.md) |
| код приглашения | `ABCD-EFGH-IJKL` (пусто — узел останется одиночным) |
| адрес сервера | `https://mn.example.com/6rfjdqfn5nno` — вместе с префиксом |

Все ответы запоминаются: прерванный запуск продолжается с того места, где остановился, а `Enter`
принимает уже введённое. Затем мастер ставит Xray, пишет конфигурацию ядра, выпускает сертификат,
рисует маскировочный сайт, запускает службы, регистрирует узел и, если WARP включён, проверяет, что
туннель действительно несёт трафик.

Вариант одной командой, без вопросов, есть в документе [Как пользоваться узлом](using-uv.md#install):

```bash
sh uv.sh --version "$RELEASE" --domain node-a.example.com --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL
```

## 3. Подтвердить узел

Режим по умолчанию — `invite+approval`: узел регистрируется и ждёт. Подтвердите его:

```bash
termit-mn uv list                        # он в списке как pending
termit-mn uv approve node-a.example.com
```

(`termit-mn` без аргументов открывает те же действия нумерованным меню, по-русски или по-английски.)

## 4. Подключить телефон

```bash
termit-uv status
```

Строка `subscription:` — это адрес для клиента, и это единственное, что вы вставляете. Откройте его в
HAPP, Hiddify, v2rayNG или любом клиенте, понимающем VLESS + XHTTP + TLS; профиль рисует узел, и он
указывает на кнопку, которую вы только что подтвердили. Там же виден список участников, так что второе
устройство или второй узел появляются без отдельного шага.

## 5. Что сохранить

| Что | Где | Зачем |
|---|---|---|
| админский токен | `/etc/termit/mn/admin.token` | нужен каждой команде `termit-mn`; `termit-mn admin rotate` его меняет |
| мастер-ключ | `/etc/termit/mn/master.key` | без него база не выведет ни одного парного пароля — сбросить его нельзя |
| база | `/var/lib/termit/mn/termit.db` | реестр: узлы, пары, пользователи, инвайты |

Три вещи сохраняйте вместе и подальше от самого сервера.

## Куда дальше

- [Как пользоваться узлом](using-uv.md) и [Как запустить сервер](using-mn.md) — все команды на каждый день.
- [Интерактивное меню](menu.md) — то же самое нумерованным меню вместо флагов.
- [Защита узла](hardening.md) — межсетевой экран, бан-движок и путь назад.
- [`docs/OPERATIONS.md`](../OPERATIONS.md) — обновление, резервные копии, восстановление, мониторинг.
- [Если что-то пошло не так](troubleshooting.md) — частые отказы и что с ними делать.
