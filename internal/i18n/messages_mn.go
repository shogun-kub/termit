package i18n

// The server's menu. The node's catalog lives in messages.go; these are merged with it, so a key is
// available to both binaries and a test walks the union.

const (
	MnTitle     Key = "mn.title"
	MnNoToken   Key = "mn.no_token"
	MnTokenHint Key = "mn.token_hint"
	MnTokenFrom Key = "mn.token_from"
	MnBadChoice Key = "mn.bad_choice"

	MnEntryStatusLabel   Key = "mn.entry.status.label"
	MnEntryStatusHint    Key = "mn.entry.status.hint"
	MnEntryNodesLabel    Key = "mn.entry.nodes.label"
	MnEntryNodesHint     Key = "mn.entry.nodes.hint"
	MnEntryInvitesLabel  Key = "mn.entry.invites.label"
	MnEntryInvitesHint   Key = "mn.entry.invites.hint"
	MnEntryCheckLabel    Key = "mn.entry.check.label"
	MnEntryCheckHint     Key = "mn.entry.check.hint"
	MnEntryPolicyLabel   Key = "mn.entry.policy.label"
	MnEntryPolicyHint    Key = "mn.entry.policy.hint"
	MnEntrySubLabel      Key = "mn.entry.sub.label"
	MnEntrySubHint       Key = "mn.entry.sub.hint"
	MnEntryAdminLabel    Key = "mn.entry.admin.label"
	MnEntryAdminHint     Key = "mn.entry.admin.hint"
	MnEntryBackupLabel   Key = "mn.entry.backup.label"
	MnEntryBackupHint    Key = "mn.entry.backup.hint"
	MnEntryHelpLabel     Key = "mn.entry.help.label"
	MnEntryHelpHint      Key = "mn.entry.help.hint"
	MnNodesHead          Key = "mn.nodes.head"
	MnNodesListLabel     Key = "mn.nodes.list.label"
	MnNodesListHint      Key = "mn.nodes.list.hint"
	MnNodesShowLabel     Key = "mn.nodes.show.label"
	MnNodesShowHint      Key = "mn.nodes.show.hint"
	MnNodesApproveLabel  Key = "mn.nodes.approve.label"
	MnNodesApproveHint   Key = "mn.nodes.approve.hint"
	MnNodesRejectLabel   Key = "mn.nodes.reject.label"
	MnNodesRejectHint    Key = "mn.nodes.reject.hint"
	MnNodesBlockLabel    Key = "mn.nodes.block.label"
	MnNodesBlockHint     Key = "mn.nodes.block.hint"
	MnNodesUnblockLabel  Key = "mn.nodes.unblock.label"
	MnNodesUnblockHint   Key = "mn.nodes.unblock.hint"
	MnNodesRemoveLabel   Key = "mn.nodes.remove.label"
	MnNodesRemoveHint    Key = "mn.nodes.remove.hint"
	MnInvitesHead        Key = "mn.invites.head"
	MnInvitesListLabel   Key = "mn.invites.list.label"
	MnInvitesListHint    Key = "mn.invites.list.hint"
	MnInvitesCreateLabel Key = "mn.invites.create.label"
	MnInvitesCreateHint  Key = "mn.invites.create.hint"
	MnInvitesRevokeLabel Key = "mn.invites.revoke.label"
	MnInvitesRevokeHint  Key = "mn.invites.revoke.hint"
	MnPolicyHead         Key = "mn.policy.head"
	MnPolicyShowLabel    Key = "mn.policy.show.label"
	MnPolicyShowHint     Key = "mn.policy.show.hint"
	MnPolicySetLabel     Key = "mn.policy.set.label"
	MnPolicySetHint      Key = "mn.policy.set.hint"
	MnSubHead            Key = "mn.sub.head"
	MnSubRotateLabel     Key = "mn.sub.rotate.label"
	MnSubRotateHint      Key = "mn.sub.rotate.hint"
	MnPromptNode         Key = "mn.prompt.node"
	MnPromptReason       Key = "mn.prompt.reason"
	MnPromptNote         Key = "mn.prompt.note"
	MnPromptInviteCode   Key = "mn.prompt.invite_code"
	MnPromptCheckAll     Key = "mn.prompt.check_all"
	MnPromptSetting      Key = "mn.prompt.setting"
	MnPromptValue        Key = "mn.prompt.value"
	MnPromptRotateAdmin  Key = "mn.prompt.rotate_admin"
	MnPromptTokenFile    Key = "mn.prompt.token_file"
	MnPromptPublicName   Key = "mn.prompt.public_name"
	MnErrNodeRequired    Key = "mn.err.node_required"
	MnErrCodeRequired    Key = "mn.err.code_required"
	MnErrSettingRequired Key = "mn.err.setting_required"
	MnErrDomainRequired  Key = "mn.err.domain_required"
)

var messagesMNRU = map[Key]string{
	MnTitle:     "Termit — сервер",
	MnNoToken:   "admin-токена нет: административные действия откажутся работать",
	MnTokenHint: "передайте --token-file <путь> в меню или задайте %s / %s",
	MnTokenFrom: "admin-токен из %s",
	MnBadChoice: "%q не из списка",

	MnEntryStatusLabel:  "состояние",
	MnEntryStatusHint:   "сервер, узлы и расписание проверок",
	MnEntryNodesLabel:   "узлы",
	MnEntryNodesHint:    "список, подробности, одобрение, блокировка, удаление",
	MnEntryInvitesLabel: "приглашения",
	MnEntryInvitesHint:  "создать, показать, отозвать коды",
	MnEntryCheckLabel:   "проверить сейчас",
	MnEntryCheckHint:    "один узел или весь mesh",
	MnEntryPolicyLabel:  "политика",
	MnEntryPolicyHint:   "показать и изменить пороги планировщика",
	MnEntrySubLabel:     "подписка",
	MnEntrySubHint:      "показать или сменить токен подписки узла",
	MnEntryAdminLabel:   "admin-токен",
	MnEntryAdminHint:    "сменить учётные данные всех админ-команд",
	MnEntryBackupLabel:  "резервная копия",
	MnEntryBackupHint:   "записать согласованный снимок",
	MnEntryHelpLabel:    "справка",
	MnEntryHelpHint:     "все команды и флаги",

	MnNodesHead:         "узлы",
	MnNodesListLabel:    "список",
	MnNodesListHint:     "домен, состояние, последняя проверка",
	MnNodesShowLabel:    "показать",
	MnNodesShowHint:     "всё про один узел",
	MnNodesApproveLabel: "одобрить",
	MnNodesApproveHint:  "принять ожидающий узел",
	MnNodesRejectLabel:  "отклонить",
	MnNodesRejectHint:   "отказать ожидающему узлу",
	MnNodesBlockLabel:   "заблокировать",
	MnNodesBlockHint:    "перестать отдавать его в подписках",
	MnNodesUnblockLabel: "разблокировать",
	MnNodesUnblockHint:  "вернуть его в mesh",
	MnNodesRemoveLabel:  "удалить",
	MnNodesRemoveHint:   "убрать из реестра",

	MnInvitesHead:        "приглашения",
	MnInvitesListLabel:   "список",
	MnInvitesListHint:    "коды, остаток использований и срок",
	MnInvitesCreateLabel: "создать",
	MnInvitesCreateHint:  "напечатать код один раз для нового узла",
	MnInvitesRevokeLabel: "отозвать",
	MnInvitesRevokeHint:  "сделать код непригодным",

	MnPolicyHead:      "политика",
	MnPolicyShowLabel: "показать",
	MnPolicyShowHint:  "пороги в силе и откуда они",
	MnPolicySetLabel:  "изменить",
	MnPolicySetHint:   "поменять один порог",

	MnSubHead:        "подписка",
	MnSubRotateLabel: "сменить токен",
	MnSubRotateHint:  "напечатать новый токен; старый URL перестанет работать",

	MnPromptNode:         "ID или домен узла",
	MnPromptReason:       "Причина, она попадёт в историю состояния",
	MnPromptNote:         "Заметка, чтобы помнить, для кого он",
	MnPromptInviteCode:   "Код приглашения для отзыва",
	MnPromptCheckAll:     "Проверить все узлы вместо одного?",
	MnPromptSetting:      "Настройка, например check_interval",
	MnPromptValue:        "Значение, пусто — снять переопределение",
	MnPromptRotateAdmin:  "Сменить admin-токен? Все скрипты и таймеры с ним перестанут работать",
	MnPromptTokenFile:    "Записать новый токен в файл (пусто — напечатать один раз)",
	MnPromptPublicName:   "Публичное имя сервера для URL (пусто — не включать)",
	MnErrNodeRequired:    "нужен ID или домен узла",
	MnErrCodeRequired:    "нужен код",
	MnErrSettingRequired: "нужно имя настройки",
	MnErrDomainRequired:  "нужен домен",
}

var messagesMNEN = map[Key]string{
	MnTitle:     "Termit — server",
	MnNoToken:   "no admin token: admin actions will refuse to run",
	MnTokenHint: "pass --token-file <path> to the menu, or export %s / %s",
	MnTokenFrom: "admin token from %s",
	MnBadChoice: "%q is not one of the choices",

	MnEntryStatusLabel:  "status",
	MnEntryStatusHint:   "server, nodes and the check schedule",
	MnEntryNodesLabel:   "nodes",
	MnEntryNodesHint:    "list, inspect, approve, block, remove",
	MnEntryInvitesLabel: "invites",
	MnEntryInvitesHint:  "create, list, revoke codes",
	MnEntryCheckLabel:   "check now",
	MnEntryCheckHint:    "one node, or the whole mesh",
	MnEntryPolicyLabel:  "policy",
	MnEntryPolicyHint:   "show and change scheduler thresholds",
	MnEntrySubLabel:     "subscription",
	MnEntrySubHint:      "show or rotate a node's subscription token",
	MnEntryAdminLabel:   "admin token",
	MnEntryAdminHint:    "rotate the credential every admin command uses",
	MnEntryBackupLabel:  "backup",
	MnEntryBackupHint:   "write a consistent snapshot",
	MnEntryHelpLabel:    "help",
	MnEntryHelpHint:     "every command and flag",

	MnNodesHead:         "nodes",
	MnNodesListLabel:    "list",
	MnNodesListHint:     "domain, state, last check",
	MnNodesShowLabel:    "show",
	MnNodesShowHint:     "everything about one node",
	MnNodesApproveLabel: "approve",
	MnNodesApproveHint:  "accept a pending node",
	MnNodesRejectLabel:  "reject",
	MnNodesRejectHint:   "refuse a pending node",
	MnNodesBlockLabel:   "block",
	MnNodesBlockHint:    "stop serving it in subscriptions",
	MnNodesUnblockLabel: "unblock",
	MnNodesUnblockHint:  "let it back in",
	MnNodesRemoveLabel:  "remove",
	MnNodesRemoveHint:   "drop it from the registry",

	MnInvitesHead:        "invites",
	MnInvitesListLabel:   "list",
	MnInvitesListHint:    "codes, uses left and expiry",
	MnInvitesCreateLabel: "create",
	MnInvitesCreateHint:  "print a code once for one new node",
	MnInvitesRevokeLabel: "revoke",
	MnInvitesRevokeHint:  "make a code unusable",

	MnPolicyHead:      "policy",
	MnPolicyShowLabel: "show",
	MnPolicyShowHint:  "thresholds in force and where they come from",
	MnPolicySetLabel:  "set",
	MnPolicySetHint:   "change one threshold",

	MnSubHead:        "subscription",
	MnSubRotateLabel: "rotate",
	MnSubRotateHint:  "print a new token; the old URL stops working",

	MnPromptNode:         "Node id or domain",
	MnPromptReason:       "Reason, recorded with the state change",
	MnPromptNote:         "Note, so you remember who it is for",
	MnPromptInviteCode:   "Invite code to revoke",
	MnPromptCheckAll:     "Check every node instead of one?",
	MnPromptSetting:      "Setting, for example check_interval",
	MnPromptValue:        "Value, empty to clear the override",
	MnPromptRotateAdmin:  "Rotate the admin token? Every script and timer using it stops working now",
	MnPromptTokenFile:    "Write the new token to this file (empty to print it once)",
	MnPromptPublicName:   "Server's public name, for the URL (empty to leave it out)",
	MnErrNodeRequired:    "a node id or domain is required",
	MnErrCodeRequired:    "a code is required",
	MnErrSettingRequired: "a setting name is required",
	MnErrDomainRequired:  "a domain is required",
}
