package i18n

// The server's menu. The node's catalog lives in messages.go; these are merged with it, so a key is
// available to both binaries and a test walks the union.

const (
	MnTitle     Key = "mn.title"
	MnNoToken   Key = "mn.no_token"
	MnTokenHint Key = "mn.token_hint"
	MnTokenFrom Key = "mn.token_from"
	MnBadChoice Key = "mn.bad_choice"

	MnEntryStatusLabel      Key = "mn.entry.status.label"
	MnEntryStatusHint       Key = "mn.entry.status.hint"
	MnEntryNodesLabel       Key = "mn.entry.nodes.label"
	MnEntryNodesHint        Key = "mn.entry.nodes.hint"
	MnEntryInvitesLabel     Key = "mn.entry.invites.label"
	MnEntryInvitesHint      Key = "mn.entry.invites.hint"
	MnEntryCheckLabel       Key = "mn.entry.check.label"
	MnEntryCheckHint        Key = "mn.entry.check.hint"
	MnEntryPolicyLabel      Key = "mn.entry.policy.label"
	MnEntryPolicyHint       Key = "mn.entry.policy.hint"
	MnEntrySubLabel         Key = "mn.entry.sub.label"
	MnEntrySubHint          Key = "mn.entry.sub.hint"
	MnEntryAdminLabel       Key = "mn.entry.admin.label"
	MnEntryAdminHint        Key = "mn.entry.admin.hint"
	MnEntryBackupLabel      Key = "mn.entry.backup.label"
	MnEntryBackupHint       Key = "mn.entry.backup.hint"
	MnEntryUpdateLabel      Key = "mn.entry.update.label"
	MnEntryUpdateHint       Key = "mn.entry.update.hint"
	MnPromptUpdate          Key = "mn.prompt.update"
	MnEntryHelpLabel        Key = "mn.entry.help.label"
	MnEntryHelpHint         Key = "mn.entry.help.hint"
	MnEntryFirewallLabel    Key = "mn.entry.firewall.label"
	MnEntryFirewallHint     Key = "mn.entry.firewall.hint"
	MnEntryBansLabel        Key = "mn.entry.bans.label"
	MnEntryBansHint         Key = "mn.entry.bans.hint"
	MnEntryGuardLabel       Key = "mn.entry.guard.label"
	MnEntryGuardHint        Key = "mn.entry.guard.hint"
	MnEntryRestoreLabel     Key = "mn.entry.restore.label"
	MnEntryRestoreHint      Key = "mn.entry.restore.hint"
	MnNodesHead             Key = "mn.nodes.head"
	MnNodesListLabel        Key = "mn.nodes.list.label"
	MnNodesListHint         Key = "mn.nodes.list.hint"
	MnNodesShowLabel        Key = "mn.nodes.show.label"
	MnNodesShowHint         Key = "mn.nodes.show.hint"
	MnNodesApproveLabel     Key = "mn.nodes.approve.label"
	MnNodesApproveHint      Key = "mn.nodes.approve.hint"
	MnNodesRejectLabel      Key = "mn.nodes.reject.label"
	MnNodesRejectHint       Key = "mn.nodes.reject.hint"
	MnNodesBlockLabel       Key = "mn.nodes.block.label"
	MnNodesBlockHint        Key = "mn.nodes.block.hint"
	MnNodesUnblockLabel     Key = "mn.nodes.unblock.label"
	MnNodesUnblockHint      Key = "mn.nodes.unblock.hint"
	MnNodesRemoveLabel      Key = "mn.nodes.remove.label"
	MnNodesRemoveHint       Key = "mn.nodes.remove.hint"
	MnNodesReissueLabel     Key = "mn.nodes.reissue.label"
	MnNodesReissueHint      Key = "mn.nodes.reissue.hint"
	MnNodesAgentKeyLabel    Key = "mn.nodes.agentkey.label"
	MnNodesAgentKeyHint     Key = "mn.nodes.agentkey.hint"
	MnNodesForgetLabel      Key = "mn.nodes.forget.label"
	MnNodesForgetHint       Key = "mn.nodes.forget.hint"
	MnInvitesHead           Key = "mn.invites.head"
	MnInvitesListLabel      Key = "mn.invites.list.label"
	MnInvitesListHint       Key = "mn.invites.list.hint"
	MnInvitesCreateLabel    Key = "mn.invites.create.label"
	MnInvitesCreateHint     Key = "mn.invites.create.hint"
	MnInvitesPolicyLabel    Key = "mn.invites.policy.label"
	MnInvitesPolicyHint     Key = "mn.invites.policy.hint"
	MnPolicyByInviteLabel   Key = "mn.policy.mode.invite.label"
	MnPolicyByInviteHint    Key = "mn.policy.mode.invite.hint"
	MnPolicyByApprovalLabel Key = "mn.policy.mode.approval.label"
	MnPolicyByApprovalHint  Key = "mn.policy.mode.approval.hint"
	MnPolicyOpenLabel       Key = "mn.policy.mode.open.label"
	MnPolicyOpenHint        Key = "mn.policy.mode.open.hint"
	MnPromptOpenMode        Key = "mn.prompt.open_mode"
	MnInvitesRevokeLabel    Key = "mn.invites.revoke.label"
	MnInvitesRevokeHint     Key = "mn.invites.revoke.hint"
	MnPolicyHead            Key = "mn.policy.head"
	MnPolicyShowLabel       Key = "mn.policy.show.label"
	MnPolicyShowHint        Key = "mn.policy.show.hint"
	MnPolicySetLabel        Key = "mn.policy.set.label"
	MnPolicySetHint         Key = "mn.policy.set.hint"
	MnSubHead               Key = "mn.sub.head"
	MnSubRotateLabel        Key = "mn.sub.rotate.label"
	MnSubRotateHint         Key = "mn.sub.rotate.hint"
	MnGroupStateLabel       Key = "mn.group.state.label"
	MnGroupStateHint        Key = "mn.group.state.hint"
	MnGroupDefenceLabel     Key = "mn.group.defence.label"
	MnGroupDefenceHint      Key = "mn.group.defence.hint"
	MnGroupServiceLabel     Key = "mn.group.service.label"
	MnGroupServiceHint      Key = "mn.group.service.hint"
	MnPromptNode            Key = "mn.prompt.node"
	MnPickNodePrompt        Key = "mn.pick_node.prompt"
	MnPickNodeCancel        Key = "mn.pick_node.cancel"
	MnPickNodeNone          Key = "mn.pick_node.none"
	MnPickNodeOffline       Key = "mn.pick_node.offline"
	MnPickNodeMiss          Key = "mn.pick_node.miss"
	MnPickNodeMany          Key = "mn.pick_node.many"
	MnPromptReason          Key = "mn.prompt.reason"
	MnPromptNote            Key = "mn.prompt.note"
	MnPromptInviteCode      Key = "mn.prompt.invite_code"
	MnPromptCheckAll        Key = "mn.prompt.check_all"
	MnPromptSetting         Key = "mn.prompt.setting"
	MnPromptValue           Key = "mn.prompt.value"
	MnPromptRotateAdmin     Key = "mn.prompt.rotate_admin"
	MnPromptTokenFile       Key = "mn.prompt.token_file"
	MnPromptPublicName      Key = "mn.prompt.public_name"
)

var messagesMNRU = map[Key]string{
	MnTitle:     "Termit — сервер",
	MnNoToken:   "admin-токена нет: административные действия откажутся работать",
	MnTokenHint: "передайте --token-file <путь> в меню или задайте %s / %s",
	MnTokenFrom: "admin-токен из %s",
	MnBadChoice: "%q не из списка",

	MnEntryStatusLabel:   "состояние",
	MnEntryStatusHint:    "сервер, узлы и расписание проверок",
	MnEntryNodesLabel:    "узлы",
	MnEntryNodesHint:     "список, подробности, одобрение, блокировка, удаление, перевыпуск",
	MnEntryInvitesLabel:  "приглашения",
	MnEntryInvitesHint:   "создать, показать, отозвать коды",
	MnEntryCheckLabel:    "проверить сейчас",
	MnEntryCheckHint:     "один узел или весь mesh",
	MnEntryPolicyLabel:   "политика",
	MnEntryPolicyHint:    "показать и изменить пороги планировщика",
	MnEntrySubLabel:      "подписка",
	MnEntrySubHint:       "показать или сменить токен подписки узла",
	MnEntryAdminLabel:    "admin-токен",
	MnEntryAdminHint:     "сменить учётные данные всех админ-команд",
	MnEntryBackupLabel:   "резервная копия",
	MnEntryBackupHint:    "записать согласованный снимок",
	MnEntryUpdateLabel:   "обновить сервер",
	MnEntryUpdateHint:    "поставить свежий релиз и перезапустить сервер; прошлый бинарник остаётся рядом",
	MnPromptUpdate:       "Заменить бинарник сервера и перезапустить его?",
	MnEntryHelpLabel:     "справка",
	MnEntryHelpHint:      "все команды и флаги",
	MnEntryFirewallLabel: "межсетевой экран",
	MnEntryFirewallHint:  "таблица хоста: состояние, включить, выключить, подтвердить",
	MnEntryBansLabel:     "блокировки",
	MnEntryBansHint:      "кого заблокировал страж и что заблокировали вы",
	MnEntryGuardLabel:    "движок блокировок",
	MnEntryGuardHint:     "работает ли страж и сколько он заблокировал",
	MnEntryRestoreLabel:  "восстановить из копии",
	MnEntryRestoreHint:   "вернуть базу и сертификаты из архива; сервер сначала нужно остановить",

	MnNodesHead:          "узлы",
	MnNodesListLabel:     "список",
	MnNodesListHint:      "домен, состояние, последняя проверка",
	MnNodesShowLabel:     "показать",
	MnNodesShowHint:      "всё про один узел",
	MnNodesApproveLabel:  "одобрить",
	MnNodesApproveHint:   "принять ожидающий узел",
	MnNodesRejectLabel:   "отклонить",
	MnNodesRejectHint:    "отказать ожидающему узлу",
	MnNodesBlockLabel:    "заблокировать",
	MnNodesBlockHint:     "перестать отдавать его в подписках",
	MnNodesUnblockLabel:  "разблокировать",
	MnNodesUnblockHint:   "вернуть его в mesh",
	MnNodesRemoveLabel:   "удалить",
	MnNodesRemoveHint:    "убрать из реестра",
	MnNodesReissueLabel:  "выпустить заново",
	MnNodesReissueHint:   "новая идентичность узла; старый ключ сразу перестаёт работать",
	MnNodesAgentKeyLabel: "ключ агента",
	MnNodesAgentKeyHint:  "показать текущий ключ, не ломая узел",
	MnNodesForgetLabel:   "забыть запись",
	MnNodesForgetHint:    "удалить запись домена, чтобы его можно было зарегистрировать заново",

	MnInvitesHead:           "приглашения",
	MnInvitesListLabel:      "список",
	MnInvitesListHint:       "коды, остаток использований и срок",
	MnInvitesCreateLabel:    "создать",
	MnInvitesCreateHint:     "напечатать код один раз для нового узла",
	MnInvitesPolicyLabel:    "правила вступления",
	MnInvitesPolicyHint:     "кого сервер пускает и с чьего разрешения",
	MnPolicyByInviteLabel:   "по приглашению",
	MnPolicyByInviteHint:    "код от оператора, и узел сразу активен после проверок",
	MnPolicyByApprovalLabel: "по приглашению и одобрению",
	MnPolicyByApprovalHint:  "код от оператора, но узел ждёт одобрения",
	MnPolicyOpenLabel:       "открыто, без приглашения",
	MnPolicyOpenHint:        "каждый, кто знает адрес, может зарегистрироваться",
	MnPromptOpenMode:        "Открыть вступление без приглашений? Любой, кто узнает адрес, станет узлом", MnInvitesRevokeLabel: "отозвать",
	MnInvitesRevokeHint: "сделать код непригодным",

	MnPolicyHead:      "политика",
	MnPolicyShowLabel: "показать",
	MnPolicyShowHint:  "пороги в силе и откуда они",
	MnPolicySetLabel:  "изменить",
	MnPolicySetHint:   "поменять один порог",

	MnSubHead:        "подписка",
	MnSubRotateLabel: "сменить токен",
	MnSubRotateHint:  "напечатать новый токен; старый URL перестанет работать",

	MnGroupStateLabel:   "Состояние",
	MnGroupStateHint:    "сервер, проверка, пороги, страж",
	MnGroupDefenceLabel: "Защита",
	MnGroupDefenceHint:  "межсетевой экран и баны",
	MnGroupServiceLabel: "Обслуживание",
	MnGroupServiceHint:  "токен, резервные копии, восстановление",
	MnPromptNode:        "Узел: домен, ID или часть домена (пусто — отмена)",
	MnPickNodePrompt:    "какой узел?",
	MnPickNodeCancel:    "отмена",
	MnPickNodeNone:      "в сети пока нет узлов",
	MnPickNodeOffline:   "сервер не ответил, списка нет: введите домен или ID вручную",
	MnPickNodeMiss:      "нет такого узла: %s",
	MnPickNodeMany:      "под это подходит несколько узлов (%d) — выберите номер из списка",
	MnPromptReason:      "Причина, она попадёт в историю состояния",
	MnPromptNote:        "Заметка, чтобы помнить, для кого он",
	MnPromptInviteCode:  "Код приглашения для отзыва (пусто — отмена)",
	MnPromptCheckAll:    "Проверить все узлы вместо одного?",
	MnPromptSetting:     "Настройка, например check_interval",
	MnPromptValue:       "Значение, пусто — снять переопределение",
	MnPromptRotateAdmin: "Сменить admin-токен? Все скрипты и таймеры с ним перестанут работать",
	MnPromptTokenFile:   "Записать новый токен в файл (пусто — напечатать один раз)",
	MnPromptPublicName:  "Публичное имя сервера для URL (пусто — не включать)",
}

var messagesMNEN = map[Key]string{
	MnTitle:     "Termit — server",
	MnNoToken:   "no admin token: admin actions will refuse to run",
	MnTokenHint: "pass --token-file <path> to the menu, or export %s / %s",
	MnTokenFrom: "admin token from %s",
	MnBadChoice: "%q is not one of the choices",

	MnEntryStatusLabel:   "status",
	MnEntryStatusHint:    "server, nodes and the check schedule",
	MnEntryNodesLabel:    "nodes",
	MnEntryNodesHint:     "list, inspect, approve, block, remove, reissue, forget",
	MnEntryInvitesLabel:  "invites",
	MnEntryInvitesHint:   "create, list, revoke codes",
	MnEntryCheckLabel:    "check now",
	MnEntryCheckHint:     "one node, or the whole mesh",
	MnEntryPolicyLabel:   "policy",
	MnEntryPolicyHint:    "show and change scheduler thresholds",
	MnEntrySubLabel:      "subscription",
	MnEntrySubHint:       "show or rotate a node's subscription token",
	MnEntryAdminLabel:    "admin token",
	MnEntryAdminHint:     "rotate the credential every admin command uses",
	MnEntryBackupLabel:   "backup",
	MnEntryBackupHint:    "write a consistent snapshot",
	MnEntryUpdateLabel:   "update the server",
	MnEntryUpdateHint:    "install the newest release and restart; the previous binary stays beside it",
	MnPromptUpdate:       "Replace the server's binary and restart it?",
	MnEntryHelpLabel:     "help",
	MnEntryHelpHint:      "every command and flag",
	MnEntryFirewallLabel: "firewall",
	MnEntryFirewallHint:  "the host table: status, on, off, confirm",
	MnEntryBansLabel:     "bans",
	MnEntryBansHint:      "what the guard banned, and what you blocked yourself",
	MnEntryGuardLabel:    "guard",
	MnEntryGuardHint:     "whether the ban engine runs, and how much it banned",
	MnEntryRestoreLabel:  "restore from a snapshot",
	MnEntryRestoreHint:   "put the database and certificates back from an archive; stop the server first",

	MnNodesHead:          "nodes",
	MnNodesListLabel:     "list",
	MnNodesListHint:      "domain, state, last check",
	MnNodesShowLabel:     "show",
	MnNodesShowHint:      "everything about one node",
	MnNodesApproveLabel:  "approve",
	MnNodesApproveHint:   "accept a pending node",
	MnNodesRejectLabel:   "reject",
	MnNodesRejectHint:    "refuse a pending node",
	MnNodesBlockLabel:    "block",
	MnNodesBlockHint:     "stop serving it in subscriptions",
	MnNodesUnblockLabel:  "unblock",
	MnNodesUnblockHint:   "let it back in",
	MnNodesRemoveLabel:   "remove",
	MnNodesRemoveHint:    "drop it from the registry",
	MnNodesReissueLabel:  "reissue",
	MnNodesReissueHint:   "a new identity for the node; the old key stops at once",
	MnNodesAgentKeyLabel: "agent key",
	MnNodesAgentKeyHint:  "print the current key without breaking the node",
	MnNodesForgetLabel:   "forget",
	MnNodesForgetHint:    "drop the record so the domain can be enrolled again",

	MnInvitesHead:           "invites",
	MnInvitesListLabel:      "list",
	MnInvitesListHint:       "codes, uses left and expiry",
	MnInvitesCreateLabel:    "create",
	MnInvitesCreateHint:     "print a code once for one new node",
	MnInvitesPolicyLabel:    "enrollment rules",
	MnInvitesPolicyHint:     "who the server lets in, and on whose word",
	MnPolicyByInviteLabel:   "by invite",
	MnPolicyByInviteHint:    "a code from the operator, and the node is active once its checks pass",
	MnPolicyByApprovalLabel: "by invite and approval",
	MnPolicyByApprovalHint:  "a code from the operator, and the node waits for an approval",
	MnPolicyOpenLabel:       "open, no invite",
	MnPolicyOpenHint:        "anyone who knows the address can register",
	MnPromptOpenMode:        "Open enrollment to anyone? Whoever learns the address becomes a node", MnInvitesRevokeLabel: "revoke",
	MnInvitesRevokeHint: "make a code unusable",

	MnPolicyHead:      "policy",
	MnPolicyShowLabel: "show",
	MnPolicyShowHint:  "thresholds in force and where they come from",
	MnPolicySetLabel:  "set",
	MnPolicySetHint:   "change one threshold",

	MnSubHead:        "subscription",
	MnSubRotateLabel: "rotate",
	MnSubRotateHint:  "print a new token; the old URL stops working",

	MnGroupStateLabel:   "State",
	MnGroupStateHint:    "server, check, thresholds, the guard",
	MnGroupDefenceLabel: "Defence",
	MnGroupDefenceHint:  "firewall and bans",
	MnGroupServiceLabel: "Service",
	MnGroupServiceHint:  "token, backups, restore",
	MnPromptNode:        "Node: domain, id or part of a domain (empty cancels)",
	MnPickNodePrompt:    "which node?",
	MnPickNodeCancel:    "cancel",
	MnPickNodeNone:      "the mesh has no nodes yet",
	MnPickNodeOffline:   "the server did not answer, so there is no list: type the domain or id",
	MnPickNodeMiss:      "no such node: %s",
	MnPickNodeMany:      "that matches %d nodes — pick one by its number",
	MnPromptReason:      "Reason, recorded with the state change",
	MnPromptNote:        "Note, so you remember who it is for",
	MnPromptInviteCode:  "Invite code to revoke (empty cancels)",
	MnPromptCheckAll:    "Check every node instead of one?",
	MnPromptSetting:     "Setting, for example check_interval",
	MnPromptValue:       "Value, empty to clear the override",
	MnPromptRotateAdmin: "Rotate the admin token? Every script and timer using it stops working now",
	MnPromptTokenFile:   "Write the new token to this file (empty to print it once)",
	MnPromptPublicName:  "Server's public name, for the URL (empty to leave it out)",
}
