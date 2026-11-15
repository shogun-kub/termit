package i18n

// Key names one message. The compiler cannot check that a key exists in both catalogs, so a test does:
// a key present in one language and missing in the other is a broken menu waiting to happen.
type Key string

const (
	// The interactive menu.
	MenuTitle        Key = "menu.title"
	MenuNotInstalled Key = "menu.not_installed"
	MenuNoState      Key = "menu.no_state"
	MenuInstalled    Key = "menu.installed"
	MenuConfigError  Key = "menu.config_error"
	MenuStateError   Key = "menu.state_error"
	MenuCancelled    Key = "menu.cancelled"
	MenuPastEnd      Key = "menu.past_end"
	MenuStepFailed   Key = "menu.step_failed"
	MenuNoConfig     Key = "menu.no_config"

	// The menu entries: a label and a hint each.
	MenuStatusLabel      Key = "menu.status.label"
	MenuStatusHint       Key = "menu.status.hint"
	MenuDoctorLabel      Key = "menu.doctor.label"
	MenuDoctorHint       Key = "menu.doctor.hint"
	MenuNetLabel         Key = "menu.net.label"
	MenuNetHint          Key = "menu.net.hint"
	MenuInstallLabel     Key = "menu.install.label"
	MenuInstallHint      Key = "menu.install.hint"
	MenuWizardLabel      Key = "menu.wizard.label"
	MenuWizardHint       Key = "menu.wizard.hint"
	MenuOptionsLabel     Key = "menu.options.label"
	MenuOptionsHint      Key = "menu.options.hint"
	MenuEnrollLabel      Key = "menu.enroll.label"
	MenuEnrollHint       Key = "menu.enroll.hint"
	MenuSyncLabel        Key = "menu.sync.label"
	MenuSyncHint         Key = "menu.sync.hint"
	MenuDriftLabel       Key = "menu.drift.label"
	MenuDriftHint        Key = "menu.drift.hint"
	MenuDevicesLabel     Key = "menu.devices.label"
	MenuDevicesHint      Key = "menu.devices.hint"
	MenuURILabel         Key = "menu.uri.label"
	MenuURIHint          Key = "menu.uri.hint"
	MenuWarpLabel        Key = "menu.warp.label"
	MenuWarpHint         Key = "menu.warp.hint"
	MenuCertificateLabel Key = "menu.certificate.label"
	MenuCertificateHint  Key = "menu.certificate.hint"
	MenuDecoyLabel       Key = "menu.decoy.label"
	MenuDecoyHint        Key = "menu.decoy.hint"
	MenuUninstallLabel   Key = "menu.uninstall.label"
	MenuUninstallHint    Key = "menu.uninstall.hint"
	MenuHelpLabel        Key = "menu.help.label"
	MenuHelpHint         Key = "menu.help.hint"

	// The sub-menus: the device list and the egress switch.
	SubDevicesListLabel   Key = "sub.devices.list.label"
	SubDevicesListHint    Key = "sub.devices.list.hint"
	SubDevicesAddLabel    Key = "sub.devices.add.label"
	SubDevicesAddHint     Key = "sub.devices.add.hint"
	SubDevicesRemoveLabel Key = "sub.devices.remove.label"
	SubDevicesRemoveHint  Key = "sub.devices.remove.hint"
	SubWarpShowLabel      Key = "sub.warp.show.label"
	SubWarpShowHint       Key = "sub.warp.show.hint"
	SubWarpAllLabel       Key = "sub.warp.all.label"
	SubWarpAllHint        Key = "sub.warp.all.hint"
	SubWarpOffLabel       Key = "sub.warp.off.label"
	SubWarpOffHint        Key = "sub.warp.off.hint"
	SubWarpSelectedLabel  Key = "sub.warp.selected.label"
	SubWarpSelectedHint   Key = "sub.warp.selected.hint"

	// Prompts the menu asks before it builds a command line.
	PromptDomain       Key = "prompt.domain"
	PromptEmail        Key = "prompt.email"
	PromptWarp         Key = "prompt.warp"
	PromptMN           Key = "prompt.mn"
	PromptInvite       Key = "prompt.invite"
	PromptDeviceLabel  Key = "prompt.device_label"
	PromptDeviceRemove Key = "prompt.device_remove"
	PromptWarpDomains  Key = "prompt.warp_domains"
	PromptCertForce    Key = "prompt.cert_force"
	PromptTemplate     Key = "prompt.template"
	PromptPurge        Key = "prompt.purge"
	PromptSure         Key = "prompt.sure"
	ErrDomainRequired  Key = "err.domain_required"

	// The wizard.
	WizardTitle          Key = "wizard.title"
	WizardHint           Key = "wizard.hint"
	WizardLang           Key = "wizard.lang"
	WizardLangInvalid    Key = "wizard.lang_invalid"
	WizardConsoleNote    Key = "wizard.console_note"
	WizardDomain         Key = "wizard.domain"
	WizardDomainNeeded   Key = "wizard.domain_needed"
	WizardDomainShape    Key = "wizard.domain_shape"
	WizardEmail          Key = "wizard.email"
	WizardTemplate       Key = "wizard.template"
	WizardTemplateIntro  Key = "wizard.template_intro"
	WizardTemplateBad    Key = "wizard.template_bad"
	WizardWarpIntro1     Key = "wizard.warp_intro1"
	WizardWarpIntro2     Key = "wizard.warp_intro2"
	WizardWarpIntro3     Key = "wizard.warp_intro3"
	WizardWarp           Key = "wizard.warp"
	WizardWarpBad        Key = "wizard.warp_bad"
	WizardWarpDomains    Key = "wizard.warp_domains"
	WizardWarpNeedsOne   Key = "wizard.warp_needs_one"
	WizardMeshIntro      Key = "wizard.mesh_intro"
	WizardMeshServer     Key = "wizard.mesh_server"
	WizardInvite         Key = "wizard.invite"
	WizardMNURL          Key = "wizard.mn_url"
	WizardMNNeeded       Key = "wizard.mn_needed"
	WizardMNShape        Key = "wizard.mn_shape"
	WizardSummary        Key = "wizard.summary"
	WizardKVIndeed       Key = "wizard.kv_invite"
	WizardConfirm        Key = "wizard.confirm"
	WizardNothingChanged Key = "wizard.nothing_changed"
	WizardInstallFailed  Key = "wizard.install_failed"
	WizardEnrollFailed   Key = "wizard.enroll_failed"
	WizardDone           Key = "wizard.done"
	WizardDoneDetail     Key = "wizard.done_detail"
	WizardInstalled      Key = "wizard.installed"
	WizardOutdated       Key = "wizard.outdated"
	WizardOutdatedHint   Key = "wizard.outdated_hint"
	WizardNothingToAsk   Key = "wizard.nothing_to_ask"
	WizardOptionsNone    Key = "wizard.options_none"
	WizardOptionsLine    Key = "wizard.options_line"
	WizardCancelled      Key = "wizard.cancelled"

	// The page of client links.
	PageTitle        Key = "page.title"
	PageSubtitle     Key = "page.subtitle"
	PageLocal        Key = "page.local"
	PageLocalNote    Key = "page.local_note"
	PageMesh         Key = "page.mesh"
	PageNoPeers      Key = "page.no_peers"
	PageWarning      Key = "page.warning"
	PageSubscription Key = "page.subscription"
	PagePlainName    Key = "page.plain_name"
	PagePlainNote    Key = "page.plain_note"
	PageJSONName     Key = "page.json_name"
	PageJSONNote     Key = "page.json_note"
	PageThisPage     Key = "page.this_page"
	PageThisPageNote Key = "page.this_page_note"
	PageCopy         Key = "page.copy"
	PageCopied       Key = "page.copied"
	PageCopyFailed   Key = "page.copy_failed"
	PageFooter       Key = "page.footer"
	PageDegraded     Key = "page.degraded"
)

// messagesRU is the default language: the operators of this project read Russian, and the guide is
// written in it.
var messagesUVRU = map[Key]string{
	MenuTitle:        "Termit — узел",
	MenuNotInstalled: "ещё не установлен: выберите «установить или доустановить»",
	MenuNoState:      "настроен, но состояния узла нет: запустите «установить или доустановить»",
	MenuInstalled:    "установлен",
	MenuConfigError:  "конфигурация: %s",
	MenuStateError:   "состояние: %s",
	MenuCancelled:    "отменено",
	MenuPastEnd:      "%q за концом меню",
	MenuStepFailed:   "этот шаг завершился со статусом %d",
	MenuNoConfig:     "На этой машине ещё нет конфигурации узла.",

	MenuStatusLabel:      "состояние",
	MenuStatusHint:       "идентичность, сертификат, соседи, синхронизация, WARP",
	MenuDoctorLabel:      "диагностика",
	MenuDoctorHint:       "проверить машину и узел, ничего не меняя",
	MenuNetLabel:         "диагностика соединений",
	MenuNetHint:          "снять счётчики TCP: теряет узел или путь?",
	MenuInstallLabel:     "установить или доустановить",
	MenuInstallHint:      "первый запуск спросит домен; последующие только перепроверяют",
	MenuWizardLabel:      "мастер установки",
	MenuWizardHint:       "пошаговая первая установка, ответы запоминаются",
	MenuOptionsLabel:     "ссылки для клиентов",
	MenuOptionsHint:      "закрытая страница со всеми ссылками и её адрес",
	MenuEnrollLabel:      "вступить в mesh",
	MenuEnrollHint:       "регистрация по коду приглашения от оператора сервера",
	MenuSyncLabel:        "синхронизировать сейчас",
	MenuSyncHint:         "забрать список соседей сразу, не ожидая суток",
	MenuDriftLabel:       "проверить расхождение",
	MenuDriftHint:        "код 4 означает, что ядро отличается от сервера",
	MenuDevicesLabel:     "устройства",
	MenuDevicesHint:      "телефоны и ноутбуки, которые пользуются только этим узлом",
	MenuURILabel:         "напечатать vless-ссылку",
	MenuURIHint:          "ссылка собственного устройства этого узла",
	MenuWarpLabel:        "выход (WARP)",
	MenuWarpHint:         "откуда уходит проксируемый трафик",
	MenuCertificateLabel: "сертификат",
	MenuCertificateHint:  "обновить TLS-сертификат сейчас",
	MenuDecoyLabel:       "сайт прикрытия",
	MenuDecoyHint:        "перегенерировать сайт, который видят посторонние",
	MenuUninstallLabel:   "удалить",
	MenuUninstallHint:    "убрать то, что создал Termit, при желании вместе с секретами",
	MenuHelpLabel:        "справка",
	MenuHelpHint:         "все команды и флаги",

	SubDevicesListLabel:   "список",
	SubDevicesListHint:    "имена и префиксы UUID",
	SubDevicesAddLabel:    "добавить",
	SubDevicesAddHint:     "создать устройство и напечатать его ссылку",
	SubDevicesRemoveLabel: "удалить",
	SubDevicesRemoveHint:  "удалить по имени",
	SubWarpShowLabel:      "показать",
	SubWarpShowHint:       "режим, что говорит warp-cli и что видит Cloudflare",
	SubWarpAllLabel:       "all",
	SubWarpAllHint:        "весь проксируемый трафик идёт через WARP (по умолчанию)",
	SubWarpOffLabel:       "off",
	SubWarpOffHint:        "выход с собственного адреса узла",
	SubWarpSelectedLabel:  "selected",
	SubWarpSelectedHint:   "только перечисленные домены",

	PromptDomain:       "Домен (имя в DNS, указывающее на эту машину)",
	PromptEmail:        "Адрес для центра сертификации",
	PromptWarp:         "Отправлять проксируемый трафик через Cloudflare WARP?",
	PromptMN:           "Адрес сервера управления",
	PromptInvite:       "Код приглашения от оператора сервера",
	PromptDeviceLabel:  "Имя нового устройства",
	PromptDeviceRemove: "Имя устройства, которое удалить",
	PromptWarpDomains:  "Домены, которые ходят через WARP, через запятую",
	PromptCertForce:    "Перевыпустить, даже если сертификат ещё не подошёл к концу?",
	PromptTemplate:     "Шаблон: product или portal (пусто — оставить текущий)",
	PromptPurge:        "Удалить также конфигурацию, состояние, сертификаты и служебного пользователя?",
	PromptSure:         "Точно удалить %s?",
	ErrDomainRequired:  "домен обязателен",

	WizardTitle:          "Termit — первая установка",
	WizardHint:           "Enter принимает значение в скобках; пустой ответ без значения означает «не сейчас».",
	WizardLang:           "Язык / Language: ru (русский) или en (english)",
	WizardLangInvalid:    "ответьте ru или en",
	WizardConsoleNote:    "консоль не в UTF-8, поэтому дальше будет по-английски; страница останется на выбранном языке",
	WizardDomain:         "Домен (имя в DNS, указывающее на эту машину)",
	WizardDomainNeeded:   "узлу нужен домен: на него выпускается сертификат",
	WizardDomainShape:    "домен — это имя без пробелов и слэшей, например node.example.com",
	WizardEmail:          "Адрес для центра сертификации (необязательно)",
	WizardTemplate:       "Сайт прикрытия",
	WizardTemplateIntro:  "Страница, которую узел показывает посетителю, который не клиент: %s.",
	WizardTemplateBad:    "%q не из тех, что умеет эта сборка",
	WizardWarpIntro1:     "Выход: all — проксируемый трафик уходит через Cloudflare WARP;",
	WizardWarpIntro2:     "        selected — только перечисленные домены; off — напрямую с этого узла.",
	WizardWarpIntro3:     "",
	WizardWarp:           "Выход (all, selected, off)",
	WizardWarpBad:        "ответьте all, selected или off",
	WizardWarpDomains:    "Домены, которые ходят через WARP, через запятую",
	WizardWarpNeedsOne:   "режиму selected нужен хотя бы один домен",
	WizardMeshIntro:      "Mesh: один сервер управления раздаёт всем клиентам общий список соседей.",
	WizardMeshServer:     "Сервер: %s",
	WizardInvite:         "Код приглашения от оператора сервера (пусто — пропустить)",
	WizardMNURL:          "Адрес сервера управления",
	WizardMNNeeded:       "код приглашения бесполезен без сервера, к которому он относится",
	WizardMNShape:        "адрес сервера начинается с https://",
	WizardSummary:        "Всё готово к установке",
	WizardKVIndeed:       "приглашение",
	WizardConfirm:        "Устанавливать узел сейчас?",
	WizardNothingChanged: "Ничего не изменено. Ответы сохранены: запустите `termit-uv wizard` снова, чтобы продолжить.",
	WizardInstallFailed:  "установка не завершилась; ответы сохранены для следующего запуска",
	WizardEnrollFailed:   "узел установлен, но в mesh пока не вошёл: исправьте приглашение и запустите `termit-uv enroll`",
	WizardDone:           "Готово",
	WizardDoneDetail:     "узел установлен",
	WizardInstalled:      "Этот узел уже установлен; спрашивать нечего.",
	WizardOutdated:       "Узел установлен, но эта сборка изменила бы его конфигурацию: %s.",
	WizardOutdatedHint:   "Запустите `termit-uv init`, чтобы привести узел к текущей сборке; после этого мастеру нечего будет спрашивать.",
	WizardNothingToAsk:   "настройка завершена",
	WizardOptionsNone:    "ссылок для клиентов пока нет — их создаст `termit-uv options`",
	WizardOptionsLine:    "ссылки для клиентов: %s",
	WizardCancelled:      "отменено; введённые ответы сохранены",

	PageTitle:        "Ссылки для клиентов — %s",
	PageSubtitle:     "Всё, что этот узел может выдать клиенту, в одном месте. Собрано %s.",
	PageLocal:        "Этот узел",
	PageLocalNote:    "Это устройство работает, даже если сервер управления недоступен.",
	PageMesh:         "Mesh (%d)",
	PageNoPeers:      "Соседей нет: узел не зарегистрирован на сервере управления.",
	PageWarning:      "Список соседей сейчас не удалось получить с сервера управления; на странице только устройство этого узла. Попробуйте `termit-uv sync` и пересоберите страницу.",
	PageSubscription: "Подписка",
	PagePlainName:    "Простой список",
	PagePlainNote:    "По одной VLESS-ссылке в строке, в base64 — это то, что клиент хочет видеть в поле «адрес подписки».",
	PageJSONName:     "Профиль sing-box",
	PageJSONNote:     "Для клиентов, которые читают JSON sing-box напрямую.",
	PageThisPage:     "Эта страница",
	PageThisPageNote: "Адрес и есть секрет: у кого он есть, тот видит эти же ссылки. Если адрес утёк — `termit-uv options --rotate` выдаст новый.",
	PageCopy:         "копировать",
	PageCopied:       "скопировано",
	PageCopyFailed:   "не удалось",
	PageFooter:       "Страница секретная: любой, кто знает её адрес, видит эти же ссылки. Не оставляйте её на скриншотах, которые уходят из ваших рук.",
	PageDegraded:     "замедлен",
}

// messagesEN is the second language and the fallback for consoles that cannot show Cyrillic.
var messagesUVEN = map[Key]string{
	MenuTitle:        "Termit — node",
	MenuNotInstalled: "not installed yet: choose “install or converge”",
	MenuNoState:      "configured, but no node state: run “install or converge”",
	MenuInstalled:    "installed",
	MenuConfigError:  "configuration: %s",
	MenuStateError:   "state: %s",
	MenuCancelled:    "cancelled",
	MenuPastEnd:      "%q is past the end of the menu",
	MenuStepFailed:   "that step exited with status %d",
	MenuNoConfig:     "This machine has no node configuration yet.",

	MenuStatusLabel:      "status",
	MenuStatusHint:       "identity, certificate, peers, sync, WARP",
	MenuDoctorLabel:      "doctor",
	MenuDoctorHint:       "check the host and the node, change nothing",
	MenuNetLabel:         "connection diagnostics",
	MenuNetHint:          "sample the TCP counters: is the node dropping, or is the path?",
	MenuInstallLabel:     "install or converge",
	MenuInstallHint:      "first run asks for the domain; later runs only re-check",
	MenuWizardLabel:      "setup wizard",
	MenuWizardHint:       "step by step through a first installation, remembering the answers",
	MenuOptionsLabel:     "client options",
	MenuOptionsHint:      "the private page with every client link, and its address",
	MenuEnrollLabel:      "join the mesh",
	MenuEnrollHint:       "enroll with an invite code from the server operator",
	MenuSyncLabel:        "sync now",
	MenuSyncHint:         "fetch the peer set immediately instead of waiting a day",
	MenuDriftLabel:       "check drift",
	MenuDriftHint:        "exit 4 tells you the core differs from the server",
	MenuDevicesLabel:     "devices",
	MenuDevicesHint:      "phones and laptops that use this node only",
	MenuURILabel:         "print vless URI",
	MenuURIHint:          "the URI of this node's own device",
	MenuWarpLabel:        "egress (WARP)",
	MenuWarpHint:         "where proxied traffic leaves",
	MenuCertificateLabel: "certificate",
	MenuCertificateHint:  "renew the TLS certificate now",
	MenuDecoyLabel:       "decoy site",
	MenuDecoyHint:        "regenerate the site the node serves to strangers",
	MenuUninstallLabel:   "uninstall",
	MenuUninstallHint:    "remove what Termit created, optionally with its secrets",
	MenuHelpLabel:        "help",
	MenuHelpHint:         "every command and flag",

	SubDevicesListLabel:   "list",
	SubDevicesListHint:    "labels and UUID prefixes",
	SubDevicesAddLabel:    "add",
	SubDevicesAddHint:     "create a device and print its URI",
	SubDevicesRemoveLabel: "remove",
	SubDevicesRemoveHint:  "delete one by label",
	SubWarpShowLabel:      "show",
	SubWarpShowHint:       "mode, what warp-cli says, and what Cloudflare sees",
	SubWarpAllLabel:       "all",
	SubWarpAllHint:        "every proxied connection goes through WARP (default)",
	SubWarpOffLabel:       "off",
	SubWarpOffHint:        "exit from this node's own address",
	SubWarpSelectedLabel:  "selected",
	SubWarpSelectedHint:   "only the domains you list",

	PromptDomain:       "Domain (a DNS name that points at this machine)",
	PromptEmail:        "Email for the certificate authority",
	PromptWarp:         "Send proxied traffic through Cloudflare WARP?",
	PromptMN:           "Management server URL",
	PromptInvite:       "Invite code from the server operator",
	PromptDeviceLabel:  "Label for the new device",
	PromptDeviceRemove: "Label of the device to remove",
	PromptWarpDomains:  "Domains that use WARP, comma separated",
	PromptCertForce:    "Reissue even if the current certificate is not due?",
	PromptTemplate:     "Archetype: product or portal (empty keeps the current one)",
	PromptPurge:        "Also delete configuration, state, certificates and the service user?",
	PromptSure:         "Really remove %s?",
	ErrDomainRequired:  "a domain is required",

	WizardTitle:          "Termit — first installation",
	WizardHint:           "Enter accepts the value in brackets; an empty answer with no value means \"not now\".",
	WizardLang:           "Language / Язык: ru (russian) or en (english)",
	WizardLangInvalid:    "answer ru or en",
	WizardConsoleNote:    "the console is not UTF-8, so the rest is in English; the page stays in the chosen language",
	WizardDomain:         "Domain (a DNS name that points at this machine)",
	WizardDomainNeeded:   "this node needs a domain: it is what the certificate is issued for",
	WizardDomainShape:    "a domain is a bare name such as node.example.com",
	WizardEmail:          "Contact address for the certificate authority (optional)",
	WizardTemplate:       "Masking site",
	WizardTemplateIntro:  "The page the node serves to a visitor who is not a client: %s.",
	WizardTemplateBad:    "%q is not one this build generates",
	WizardWarpIntro1:     "Egress: all — proxied traffic leaves through Cloudflare WARP;",
	WizardWarpIntro2:     "        selected — only the listed domains do; off — it leaves directly from this node.",
	WizardWarpIntro3:     "",
	WizardWarp:           "Egress (all, selected, off)",
	WizardWarpBad:        "answer all, selected or off",
	WizardWarpDomains:    "Domains that use WARP, comma separated",
	WizardWarpNeedsOne:   "selected mode needs at least one domain",
	WizardMeshIntro:      "The mesh: one management server hands every client the whole peer list.",
	WizardMeshServer:     "Server: %s",
	WizardInvite:         "Invite code from the server operator (empty to skip for now)",
	WizardMNURL:          "Management server URL",
	WizardMNNeeded:       "an invite code is useless without the server it belongs to",
	WizardMNShape:        "the server URL starts with https://",
	WizardSummary:        "Ready to install",
	WizardKVIndeed:       "invite",
	WizardConfirm:        "Install this node now?",
	WizardNothingChanged: "Nothing was changed. The answers are kept: run `termit-uv wizard` again to continue.",
	WizardInstallFailed:  "the installation did not finish; the answers are kept for the next run",
	WizardEnrollFailed:   "the node is installed but not in the mesh yet: fix the invite and run `termit-uv enroll`",
	WizardDone:           "Done",
	WizardDoneDetail:     "this node is installed",
	WizardInstalled:      "This node is already installed; nothing to ask.",
	WizardOutdated:       "This node is installed, but this build would change its configuration: %s.",
	WizardOutdatedHint:   "Run `termit-uv init` to converge it; after that the wizard has nothing to ask.",
	WizardNothingToAsk:   "the setup is complete",
	WizardOptionsNone:    "no client links yet — `termit-uv options` renders one",
	WizardOptionsLine:    "client options: %s",
	WizardCancelled:      "cancelled; the answers so far are kept",

	PageTitle:        "Client options — %s",
	PageSubtitle:     "Everything this node can hand to a client, in one place. Rendered %s.",
	PageLocal:        "This node",
	PageLocalNote:    "This device works even when the management server cannot be reached.",
	PageMesh:         "Mesh (%d)",
	PageNoPeers:      "No peers: this node is not enrolled with a management server.",
	PageWarning:      "The mesh could not be fetched from the management server just now; this page shows the device of this node only. Try `termit-uv sync`, then re-render.",
	PageSubscription: "Subscription",
	PagePlainName:    "Plain list",
	PagePlainNote:    "One VLESS URI per line, base64 — the field a client's “subscription URL” wants.",
	PageJSONName:     "sing-box profile",
	PageJSONNote:     "For clients that read sing-box JSON directly.",
	PageThisPage:     "This page",
	PageThisPageNote: "The address is the only secret: anyone who has it sees the same links. Rotate it with `termit-uv options --rotate` if it leaks.",
	PageCopy:         "copy",
	PageCopied:       "copied",
	PageCopyFailed:   "copy failed",
	PageFooter:       "This page is secret: anyone with its address sees the same links. Keep it out of screenshots that leave your hands.",
	PageDegraded:     "degraded",
}

// messagesRU and messagesEN are the union of the node's catalog and the server's: each binary links
// only its own half, and the translator walks everything, so a key cannot exist in one menu and be
// missing from the other language.
var (
	messagesRU = merged(messagesUVRU, messagesMNRU)
	messagesEN = merged(messagesUVEN, messagesMNEN)
)

func merged(catalogs ...map[Key]string) map[Key]string {
	out := make(map[Key]string, 256)
	for _, catalog := range catalogs {
		for key, value := range catalog {
			out[key] = value
		}
	}
	return out
}
