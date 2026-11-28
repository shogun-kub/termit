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

	// The groups: the menu's own level, one line per group.
	MenuGroupStateLabel   Key = "menu.group.state.label"
	MenuGroupStateHint    Key = "menu.group.state.hint"
	MenuGroupNetworkLabel Key = "menu.group.network.label"
	MenuGroupNetworkHint  Key = "menu.group.network.hint"
	MenuGroupClientsLabel Key = "menu.group.clients.label"
	MenuGroupClientsHint  Key = "menu.group.clients.hint"
	MenuGroupMeshLabel    Key = "menu.group.mesh.label"
	MenuGroupMeshHint     Key = "menu.group.mesh.hint"
	MenuGroupDefenceLabel Key = "menu.group.defence.label"
	MenuGroupDefenceHint  Key = "menu.group.defence.hint"
	MenuGroupSiteLabel    Key = "menu.group.site.label"
	MenuGroupSiteHint     Key = "menu.group.site.hint"
	MenuGroupNodeLabel    Key = "menu.group.node.label"
	MenuGroupNodeHint     Key = "menu.group.node.hint"
	MenuBackLabel         Key = "menu.back.label"
	MenuBackHint          Key = "menu.back.hint"

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
	MenuAgentOnceLabel   Key = "menu.agentonce.label"
	MenuAgentOnceHint    Key = "menu.agentonce.hint"
	MenuDevicesLabel     Key = "menu.devices.label"
	MenuDevicesHint      Key = "menu.devices.hint"
	MenuURILabel         Key = "menu.uri.label"
	MenuURIHint          Key = "menu.uri.hint"
	MenuWarpLabel        Key = "menu.warp.label"
	MenuWarpHint         Key = "menu.warp.hint"
	MenuTransportLabel   Key = "menu.transport.label"
	MenuTransportHint    Key = "menu.transport.hint"
	MenuCertificateLabel Key = "menu.certificate.label"
	MenuCertificateHint  Key = "menu.certificate.hint"
	MenuDecoyLabel       Key = "menu.decoy.label"
	MenuDecoyHint        Key = "menu.decoy.hint"
	MenuUninstallLabel   Key = "menu.uninstall.label"
	MenuUninstallHint    Key = "menu.uninstall.hint"
	MenuHelpLabel        Key = "menu.help.label"
	MenuHelpHint         Key = "menu.help.hint"

	// The recovery and hardening entries: a blocked node, a moved node, and the host's own defences.
	MenuUpgradeLabel   Key = "menu.upgrade.label"
	MenuUpgradeHint    Key = "menu.upgrade.hint"
	MenuUpgradeNoOffer Key = "menu.upgrade.no_offer"
	MenuUpgradeSame    Key = "menu.upgrade.same"
	PromptUpgrade      Key = "prompt.upgrade"
	MenuUnblockLabel   Key = "menu.unblock.label"
	MenuUnblockHint    Key = "menu.unblock.hint"
	MenuMigrateLabel   Key = "menu.migrate.label"
	MenuAgentKeyLabel  Key = "menu.agentkey.label"
	MenuAgentKeyHint   Key = "menu.agentkey.hint"
	MenuMigrateHint    Key = "menu.migrate.hint"
	MenuFirewallLabel  Key = "menu.firewall.label"
	MenuFirewallHint   Key = "menu.firewall.hint"
	MenuBansLabel      Key = "menu.bans.label"
	MenuBansHint       Key = "menu.bans.hint"
	MenuGuardLabel     Key = "menu.guard.label"
	MenuGuardHint      Key = "menu.guard.hint"

	// The sub-menus: the device list, the egress switch, and the two groups both binaries share.
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
	SubWarpRotateLabel    Key = "sub.warp.rotate.label"
	SubWarpRotateHint     Key = "sub.warp.rotate.hint"

	// The host table and the ban list. Both binaries run the same commands, so both menus draw these.
	SubFirewallStatusLabel  Key = "sub.firewall.status.label"
	SubFirewallStatusHint   Key = "sub.firewall.status.hint"
	SubFirewallOnLabel      Key = "sub.firewall.on.label"
	SubFirewallOnHint       Key = "sub.firewall.on.hint"
	SubFirewallOffLabel     Key = "sub.firewall.off.label"
	SubFirewallOffHint      Key = "sub.firewall.off.hint"
	SubFirewallConfirmLabel Key = "sub.firewall.confirm.label"
	SubFirewallConfirmHint  Key = "sub.firewall.confirm.hint"
	SubFirewallRefreshLabel Key = "sub.firewall.refresh.label"
	SubFirewallRefreshHint  Key = "sub.firewall.refresh.hint"
	SubFirewallRestoreLabel Key = "sub.firewall.restore.label"
	SubFirewallRestoreHint  Key = "sub.firewall.restore.hint"
	SubBansListLabel        Key = "sub.bans.list.label"
	SubBansListHint         Key = "sub.bans.list.hint"
	SubBansOffendersLabel   Key = "sub.bans.offenders.label"
	SubBansOffendersHint    Key = "sub.bans.offenders.hint"
	SubBansBlockLabel       Key = "sub.bans.block.label"
	SubBansBlockHint        Key = "sub.bans.block.hint"
	SubBansUnbanLabel       Key = "sub.bans.unban.label"
	SubBansUnbanHint        Key = "sub.bans.unban.hint"
	SubBansUnblockLabel     Key = "sub.bans.unblock.label"
	SubBansUnblockHint      Key = "sub.bans.unblock.hint"

	// Prompts the menu asks before it builds a command line.
	PromptDomain       Key = "prompt.domain"
	PromptEmail        Key = "prompt.email"
	PromptWarp         Key = "prompt.warp"
	PromptTransport    Key = "prompt.transport"
	PromptMN           Key = "prompt.mn"
	PromptInvite       Key = "prompt.invite"
	PromptDeviceLabel  Key = "prompt.device_label"
	PromptDeviceRemove Key = "prompt.device_remove"
	PromptWarpDomains  Key = "prompt.warp_domains"
	PromptCertForce    Key = "prompt.cert_force"
	PromptTemplate     Key = "prompt.template"
	PromptPurge        Key = "prompt.purge"
	PromptSure         Key = "prompt.sure"

	// Prompts for the recovery, relocation and hardening entries.
	PromptAddress         Key = "prompt.address"
	PromptNewDomain       Key = "prompt.new_domain"
	PromptAgentKeyFile    Key = "prompt.agentkey_file"
	PromptAgentKeyConfirm Key = "prompt.agentkey_confirm"
	PromptWarpRotate      Key = "prompt.warp_rotate"
	PromptArchive         Key = "prompt.archive"
	PromptRestore         Key = "prompt.restore"
	PromptReissue         Key = "prompt.reissue"
	PromptForget          Key = "prompt.forget"

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
	WizardACMEIntro      Key = "wizard.acme_intro"
	WizardACME           Key = "wizard.acme"
	WizardACMEShape      Key = "wizard.acme_shape"
	WizardACMESkip       Key = "wizard.acme_skip"
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
	WizardEnrollPending  Key = "wizard.enroll_pending"
	WizardDone           Key = "wizard.done"
	WizardHardenIntro    Key = "wizard.harden_intro"
	WizardHarden         Key = "wizard.harden"
	WizardHardenHint     Key = "wizard.harden_hint"
	WizardTransport      Key = "wizard.transport"
	WizardTransportIntro Key = "wizard.transport_intro"
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
	PageDevices      Key = "page.devices"
	PageDevicesNote  Key = "page.devices_note"
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
	PageQR           Key = "page.qr"
	PageCopied       Key = "page.copied"
	PageCopyFailed   Key = "page.copy_failed"
	PageFooter       Key = "page.footer"
	PageQuicNote     Key = "page.quic_note"
	PageEgress       Key = "page.egress"
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

	MenuGroupStateLabel:   "Состояние",
	MenuGroupStateHint:    "что это за узел и всё ли с ним в порядке",
	MenuGroupNetworkLabel: "Сеть",
	MenuGroupNetworkHint:  "счётчики, WARP, второй транспорт",
	MenuGroupClientsLabel: "Клиенты",
	MenuGroupClientsHint:  "ссылки, страница клиента, устройства",
	MenuGroupMeshLabel:    "Меш",
	MenuGroupMeshHint:     "синхронизация, вступление, снятие блокировки",
	MenuGroupDefenceLabel: "Защита",
	MenuGroupDefenceHint:  "межсетевой экран, баны, страж",
	MenuGroupSiteLabel:    "Сайт",
	MenuGroupSiteHint:     "сертификат и сайт-приманка",
	MenuGroupNodeLabel:    "Узел",
	MenuGroupNodeHint:     "установка, переезд, удаление",
	MenuBackLabel:         "← назад",
	MenuBackHint:          "вернуться на уровень выше",
	MenuStatusLabel:       "состояние",
	MenuStatusHint:        "идентичность, сертификат, соседи, синхронизация, WARP",
	MenuDoctorLabel:       "диагностика",
	MenuDoctorHint:        "проверить машину и узел, ничего не меняя",
	MenuNetLabel:          "диагностика соединений",
	MenuNetHint:           "снять счётчики TCP: теряет узел или путь?",
	MenuInstallLabel:      "установить или доустановить",
	MenuInstallHint:       "первый запуск спросит домен; последующие только перепроверяют",
	MenuWizardLabel:       "мастер установки",
	MenuWizardHint:        "пошаговая первая установка, ответы запоминаются",
	MenuOptionsLabel:      "ссылки для клиентов",
	MenuOptionsHint:       "закрытая страница со всеми ссылками и её адрес",
	MenuEnrollLabel:       "вступить в mesh",
	MenuEnrollHint:        "регистрация по коду приглашения от оператора сервера",
	MenuSyncLabel:         "синхронизировать сейчас",
	MenuSyncHint:          "забрать список соседей сразу, не ожидая суток",
	MenuDriftLabel:        "проверить расхождение",
	MenuDriftHint:         "код 4 означает, что ядро отличается от сервера",
	MenuAgentOnceLabel:    "прогнать цикл агента",
	MenuAgentOnceHint:     "самопроверка и синхронизация один раз, не ожидая таймера",
	MenuDevicesLabel:      "устройства",
	MenuDevicesHint:       "телефоны и ноутбуки, которые пользуются только этим узлом",
	MenuURILabel:          "напечатать vless-ссылку",
	MenuURIHint:           "ссылка собственного устройства этого узла",
	MenuWarpLabel:         "выход (WARP)",
	MenuWarpHint:          "откуда уходит проксируемый трафик",
	MenuTransportLabel:    "QUIC-транспорт",
	MenuTransportHint:     "Hysteria2 на UDP/443 рядом с TLS-профилем: включить или выключить",
	MenuCertificateLabel:  "сертификат",
	MenuCertificateHint:   "обновить TLS-сертификат сейчас",
	MenuDecoyLabel:        "сайт-приманка",
	MenuDecoyHint:         "перегенерировать сайт, который видят посторонние",
	MenuUninstallLabel:    "удалить",
	MenuUninstallHint:     "убрать то, что создал Termit, при желании вместе с секретами",
	MenuHelpLabel:         "справка",
	MenuHelpHint:          "все команды и флаги",

	MenuUnblockLabel:   "снять блокировку",
	MenuUnblockHint:    "попросить сервер проверить узел и вернуть его в строй",
	MenuUpgradeLabel:   "обновить узел",
	MenuUpgradeHint:    "взять релиз, который называет сервер; прошлый бинарник остаётся рядом",
	MenuUpgradeNoOffer: "Обновлять нечего: сервер ещё не назвал свой релиз (синхронизация) или назвал не релиз.",
	MenuUpgradeSame:    "Узел уже на %s, как и сервер.",
	PromptUpgrade:      "Поставить %s и дать новому бинарнику сойтись?",
	MenuMigrateLabel:   "переехать на другой домен",
	MenuAgentKeyLabel:  "вписать новый агентский ключ",
	MenuAgentKeyHint:   "после передачи узла другому дому; старый ключ сразу перестанет работать",
	MenuMigrateHint:    "перенести узел на новый домен (сценарий C)",
	MenuFirewallLabel:  "межсетевой экран",
	MenuFirewallHint:   "таблица хоста: состояние, включить, выключить, подтвердить",
	MenuBansLabel:      "блокировки",
	MenuBansHint:       "кого заблокировал страж и что заблокировали вы",
	MenuGuardLabel:     "движок блокировок",
	MenuGuardHint:      "работает ли страж и сколько он заблокировал",

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
	SubWarpRotateLabel:    "rotate",
	SubWarpRotateHint:     "новое устройство в Cloudflare; старое перестаёт работать",

	SubFirewallStatusLabel:  "состояние",
	SubFirewallStatusHint:   "что применено, что подтверждено и где конфигурация расходится",
	SubFirewallOnLabel:      "включить",
	SubFirewallOnHint:       "применить таблицу; сначала взводится пятиминутный таймер отката",
	SubFirewallOffLabel:     "выключить",
	SubFirewallOffHint:      "убрать таблицу и оставить хост как было",
	SubFirewallConfirmLabel: "подтвердить",
	SubFirewallConfirmHint:  "оставить применённое: таймер отката встаёт на место",
	SubFirewallRefreshLabel: "обновить блок-лист",
	SubFirewallRefreshHint:  "забрать сетевой блок-лист и загрузить его в таблицу",
	SubFirewallRestoreLabel: "восстановить",
	SubFirewallRestoreHint:  "вернуть таблицу последнего подтверждённого on (это делает загрузочный юнит)",
	SubBansListLabel:        "список",
	SubBansListHint:         "кого блокируют сейчас и почему",
	SubBansOffendersLabel:   "рецидивисты",
	SubBansOffendersHint:    "на ком лестница кончилась: решать вам",
	SubBansBlockLabel:       "заблокировать навсегда",
	SubBansBlockHint:        "постоянная блокировка адреса по вашей команде",
	SubBansUnbanLabel:       "снять блокировку",
	SubBansUnbanHint:        "снять блокировку; проступок остаётся в записи",
	SubBansUnblockLabel:     "снять свою блокировку",
	SubBansUnblockHint:      "снять постоянную блокировку, поставленную командой block",

	PromptDomain:       "Домен (имя в DNS, указывающее на эту машину) — целиком: поддомен вместе с доменом",
	PromptEmail:        "Адрес для центра сертификации (почта для писем об истечении сертификата)",
	PromptWarp:         "Отправлять проксируемый трафик через Cloudflare WARP?",
	PromptTransport:    "Отдавать второй транспорт по QUIC (Hysteria2 на UDP/443)?",
	PromptMN:           "Адрес сервера управления",
	PromptInvite:       "Код приглашения от оператора сервера (пусто — отмена)",
	PromptDeviceLabel:  "Имя нового устройства",
	PromptDeviceRemove: "Имя устройства, которое удалить",
	PromptWarpDomains:  "Домены, которые ходят через WARP, через запятую",
	PromptCertForce:    "Перевыпустить, даже если сертификат ещё не подошёл к концу?",
	PromptTemplate:     "Шаблон: product или portal (пусто — оставить текущий)",
	PromptPurge:        "Удалить также конфигурацию, состояние, сертификаты и служебного пользователя?",
	PromptSure:         "Точно удалить %s?",

	PromptAddress:         "Адрес (IP или подсеть)",
	PromptNewDomain:       "Новый домен (пусто — оставить текущий)",
	PromptAgentKeyFile:    "Файл с ключом (права 0600)",
	PromptAgentKeyConfirm: "Вписать ключ? Текущий перестанет работать сразу, и до синхронизации узел никому не отвечает.",
	PromptWarpRotate:      "Заменить устройство в Cloudflare? Старое перестанет работать.",
	PromptArchive:         "Путь к архиву резервной копии",
	PromptRestore:         "Заменить базу и сертификаты этим архивом? Сначала остановите termit-mn serve.",
	PromptReissue:         "Выпустить узлу новую идентичность? Старый ключ перестанет работать сразу.",
	PromptForget:          "Забыть запись об узле? Домен можно будет зарегистрировать заново.",

	WizardTitle:          "Termit — первая установка",
	WizardHint:           "Enter принимает значение в скобках; пустой ответ без значения означает «не сейчас».",
	WizardLang:           "Язык / Language: ru (русский) или en (english)",
	WizardLangInvalid:    "ответьте ru или en",
	WizardConsoleNote:    "консоль не в UTF-8, поэтому дальше будет по-английски; страница останется на выбранном языке",
	WizardDomain:         "Домен (имя в DNS, указывающее на эту машину) — целиком: поддомен вместе с доменом",
	WizardDomainNeeded:   "узлу нужен домен: на него выпускается сертификат",
	WizardDomainShape:    "домен — это имя без пробелов и слэшей, например node.example.com",
	WizardEmail:          "Адрес для центра сертификации (почта для уведомлений об истечении; необязательно)",
	WizardACMEIntro:      "Сертификат выпускает ACME-каталог. Обычно это публичный: %s. Оставьте следующий ответ пустым, чтобы использовать его; назовите другой каталог для частного центра — тестового стенда или корпоративного CA.",
	WizardACME:           "Каталог ACME (пусто — публичный; если не знаете, просто нажмите Enter)",
	WizardACMEShape:      "каталог ACME — это URL http(s)",
	WizardACMESkip:       "Не проверять сертификат ACME-сервера? Только для частного CA, которому эта машина не доверяет",
	WizardTemplate:       "Сайт-приманка",
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
	WizardEnrollFailed:   "узел установлен, но регистрация не удалась: исправьте то, на что указывает ошибка выше, и запустите `termit-uv wizard` снова — он повторит регистрацию",
	WizardEnrollPending:  "Узел установлен, но в сети его ещё нет. Завершаю регистрацию кодом, который вы дали:",
	WizardDone:           "Готово",
	WizardHardenIntro:    "Узел может закрыть себя: таблица nftables с SSH, портами 80 и 443 и остальным входящим трафиком закрытым, плюс движок банов. Она не включается сама — вы включите её одной командой, когда убедитесь, что доступ не потерян.",
	WizardHarden:         "Закрыть узел (да/нет)",
	WizardHardenHint:     "Да настроит firewalls и движок банов; таблица применится командой termit-uv firewall on",
	WizardTransport:      "Нести ещё и Hysteria2 на UDP/443 рядом с TLS-профилем (да/нет)",
	WizardTransportIntro: "QUIC проходит там, где TLS душат, и это транспорт, на котором лучше всего работают голос и видео. Ему нужен открытый UDP/443; в ссылках тогда будут оба профиля.",
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
	PageDevices:      "Устройства (%d)",
	PageDevicesNote:  "Ваши собственные устройства. Они есть только на этом узле и в mesh не уходят.",
	PageMesh:         "Меш (%d)",
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
	PageQR:           "QR-код",
	PageCopied:       "скопировано",
	PageCopyFailed:   "не удалось",
	PageFooter:       "Страница секретная: любой, кто знает её адрес, видит эти же ссылки. Не оставляйте её на скриншотах, которые уходят из ваших рук.",
	PageQuicNote:     "голос и видео",
	PageEgress:       "выход: ",
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

	MenuGroupStateLabel:   "State",
	MenuGroupStateHint:    "what this node is, and whether it is healthy",
	MenuGroupNetworkLabel: "Network",
	MenuGroupNetworkHint:  "counters, WARP, the second transport",
	MenuGroupClientsLabel: "Clients",
	MenuGroupClientsHint:  "links, the client page, devices",
	MenuGroupMeshLabel:    "Mesh",
	MenuGroupMeshHint:     "sync, enrollment, lifting a block",
	MenuGroupDefenceLabel: "Defence",
	MenuGroupDefenceHint:  "firewall, bans, the guard",
	MenuGroupSiteLabel:    "Site",
	MenuGroupSiteHint:     "certificate and the masking site",
	MenuGroupNodeLabel:    "Node",
	MenuGroupNodeHint:     "install, relocate, uninstall",
	MenuBackLabel:         "← back",
	MenuBackHint:          "up one level",
	MenuStatusLabel:       "status",
	MenuStatusHint:        "identity, certificate, peers, sync, WARP",
	MenuDoctorLabel:       "doctor",
	MenuDoctorHint:        "check the host and the node, change nothing",
	MenuNetLabel:          "connection diagnostics",
	MenuNetHint:           "sample the TCP counters: is the node dropping, or is the path?",
	MenuInstallLabel:      "install or converge",
	MenuInstallHint:       "first run asks for the domain; later runs only re-check",
	MenuWizardLabel:       "setup wizard",
	MenuWizardHint:        "step by step through a first installation, remembering the answers",
	MenuOptionsLabel:      "client options",
	MenuOptionsHint:       "the private page with every client link, and its address",
	MenuEnrollLabel:       "join the mesh",
	MenuEnrollHint:        "enroll with an invite code from the server operator",
	MenuSyncLabel:         "sync now",
	MenuSyncHint:          "fetch the peer set immediately instead of waiting a day",
	MenuDriftLabel:        "check drift",
	MenuDriftHint:         "exit 4 tells you the core differs from the server",
	MenuAgentOnceLabel:    "run one agent cycle",
	MenuAgentOnceHint:     "self-check and sync once, without waiting for the timer",
	MenuDevicesLabel:      "devices",
	MenuDevicesHint:       "phones and laptops that use this node only",
	MenuURILabel:          "print vless URI",
	MenuURIHint:           "the URI of this node's own device",
	MenuWarpLabel:         "egress (WARP)",
	MenuWarpHint:          "where proxied traffic leaves",
	MenuTransportLabel:    "QUIC transport",
	MenuTransportHint:     "Hysteria2 on UDP/443 beside the TLS profile: turn it on or off",
	MenuCertificateLabel:  "certificate",
	MenuCertificateHint:   "renew the TLS certificate now",
	MenuDecoyLabel:        "decoy site",
	MenuDecoyHint:         "regenerate the site the node serves to strangers",
	MenuUninstallLabel:    "uninstall",
	MenuUninstallHint:     "remove what Termit created, optionally with its secrets",
	MenuHelpLabel:         "help",
	MenuHelpHint:          "every command and flag",

	MenuUnblockLabel:   "lift a block",
	MenuUnblockHint:    "ask the server to verify this node and let it back in",
	MenuUpgradeLabel:   "upgrade this node",
	MenuUpgradeHint:    "take the release the server names; the previous binary stays beside it",
	MenuUpgradeNoOffer: "Nothing to upgrade to: the server has not named its release yet (sync), or named something that is not one.",
	MenuUpgradeSame:    "This node is already on %s, the same as the server.",
	PromptUpgrade:      "Install %s and let the new binary converge the node?",
	MenuMigrateLabel:   "move to another domain",
	MenuAgentKeyLabel:  "install a new agent key",
	MenuAgentKeyHint:   "after a handoff to another home; the old key stops working at once",
	MenuMigrateHint:    "relocate this node to a new domain (Scenario C)",
	MenuFirewallLabel:  "firewall",
	MenuFirewallHint:   "the host table: status, on, off, confirm",
	MenuBansLabel:      "bans",
	MenuBansHint:       "what the guard banned, and what you blocked yourself",
	MenuGuardLabel:     "guard",
	MenuGuardHint:      "whether the ban engine runs, and how much it banned",

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
	SubWarpRotateLabel:    "rotate",
	SubWarpRotateHint:     "a new Cloudflare device; the old one stops working",

	SubFirewallStatusLabel:  "status",
	SubFirewallStatusHint:   "what is applied, what was confirmed, and where the configuration disagrees",
	SubFirewallOnLabel:      "on",
	SubFirewallOnHint:       "apply the table; a five-minute rollback timer is armed first",
	SubFirewallOffLabel:     "off",
	SubFirewallOffHint:      "remove the table and leave the host as it was",
	SubFirewallConfirmLabel: "confirm",
	SubFirewallConfirmHint:  "keep what `on` applied by standing the rollback timer down",
	SubFirewallRefreshLabel: "refresh",
	SubFirewallRefreshHint:  "fetch the network blocklist and load it into the table",
	SubFirewallRestoreLabel: "restore",
	SubFirewallRestoreHint:  "put back the table the last confirmed `on` applied; the boot unit runs this",
	SubBansListLabel:        "list",
	SubBansListHint:         "what is banned now, and why",
	SubBansOffendersLabel:   "offenders",
	SubBansOffendersHint:    "sources the ladder ran out of rungs for; the decision is yours",
	SubBansBlockLabel:       "block",
	SubBansBlockHint:        "ban an address permanently, by your own command",
	SubBansUnbanLabel:       "unban",
	SubBansUnbanHint:        "lift a ban; the offence stays on record",
	SubBansUnblockLabel:     "unblock",
	SubBansUnblockHint:      "lift the permanent block your own `block` put there",

	PromptDomain:       "Domain (a DNS name that points at this machine) — the whole name: subdomain and domain together",
	PromptEmail:        "Email for the certificate authority (it receives expiry notices)",
	PromptWarp:         "Send proxied traffic through Cloudflare WARP?",
	PromptTransport:    "Serve the second transport over QUIC (Hysteria2 on UDP/443)?",
	PromptMN:           "Management server URL",
	PromptInvite:       "Invite code from the server operator (empty cancels)",
	PromptDeviceLabel:  "Label for the new device",
	PromptDeviceRemove: "Label of the device to remove",
	PromptWarpDomains:  "Domains that use WARP, comma separated",
	PromptCertForce:    "Reissue even if the current certificate is not due?",
	PromptTemplate:     "Archetype: product or portal (empty keeps the current one)",
	PromptPurge:        "Also delete configuration, state, certificates and the service user?",
	PromptSure:         "Really remove %s?",

	PromptAddress:         "Address (IP or prefix)",
	PromptNewDomain:       "New domain (empty keeps the configured one)",
	PromptAgentKeyFile:    "The file holding the key (0600)",
	PromptAgentKeyConfirm: "Install the key? The current one stops working at once, and the node answers to nobody until it syncs.",
	PromptWarpRotate:      "Replace the Cloudflare device? The old one stops working.",
	PromptArchive:         "Path to the backup archive",
	PromptRestore:         "Replace the database and the certificates with this archive? Stop termit-mn serve first.",
	PromptReissue:         "Reissue this node's identity? The old key stops working at once.",
	PromptForget:          "Forget this node's record? The domain can then be enrolled again.",

	WizardTitle:          "Termit — first installation",
	WizardHint:           "Enter accepts the value in brackets; an empty answer with no value means \"not now\".",
	WizardLang:           "Language / Язык: ru (russian) or en (english)",
	WizardLangInvalid:    "answer ru or en",
	WizardConsoleNote:    "the console is not UTF-8, so the rest is in English; the page stays in the chosen language",
	WizardDomain:         "Domain (a DNS name that points at this machine) — the whole name: subdomain and domain together",
	WizardDomainNeeded:   "this node needs a domain: it is what the certificate is issued for",
	WizardDomainShape:    "a domain is a bare name such as node.example.com",
	WizardEmail:          "Contact address for the certificate authority (it receives expiry notices; optional)",
	WizardACMEIntro:      "The certificate comes from an ACME directory. Most installations use the public one: %s. Leave the next answer empty for it, or name another directory for a private authority — a test lab or a company CA.",
	WizardACME:           "ACME directory (empty = the public one; if unsure, just press Enter)",
	WizardACMEShape:      "an ACME directory is an http(s) URL",
	WizardACMESkip:       "Do not verify the ACME server's certificate? Only for a private CA this host does not trust",
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
	WizardEnrollFailed:   "the node is installed but the registration failed: fix what the error above names and run `termit-uv wizard` again, which retries it",
	WizardEnrollPending:  "The node is installed but not in the mesh yet. Finishing the registration with the code you gave:",
	WizardDone:           "Done",
	WizardHardenIntro:    "The node can close itself: an nftables table that keeps SSH, ports 80 and 443 and drops the rest of the inbound traffic, plus the ban engine. It is not switched on from here — that is one command, run when you know your access survives.",
	WizardHarden:         "Harden this node (yes/no)",
	WizardHardenHint:     "Yes configures the firewall and the ban engine; the table itself is applied by termit-uv firewall on",
	WizardTransport:      "Also serve Hysteria2 on UDP/443 beside the TLS profile (yes/no)",
	WizardTransportIntro: "QUIC gets through where TLS is throttled, and it is the transport voice and video work best on. It needs UDP/443 open; the links then carry both profiles.",
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
	PageDevices:      "Devices (%d)",
	PageDevicesNote:  "Your own devices. They exist only on this node and never reach the mesh.",
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
	PageQR:           "QR",
	PageCopied:       "copied",
	PageCopyFailed:   "copy failed",
	PageFooter:       "This page is secret: anyone with its address sees the same links. Keep it out of screenshots that leave your hands.",
	PageQuicNote:     "voice and video",
	PageEgress:       "egress: ",
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
