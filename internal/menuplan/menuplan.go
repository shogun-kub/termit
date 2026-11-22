// Package menuplan holds the two sub-menus termit-uv and termit-mn share. The host firewall and the ban
// list are the same commands in both binaries, so the questions live in one table here rather than in
// two copies that drift apart — which is the rule the menus themselves follow.
package menuplan

import (
	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/termui"
)

// Option is one line of a sub-menu and the command line it runs.
type Option struct {
	Label i18n.Key
	Hint  i18n.Key
	// Ask, when set, is the label of a value the operator has to type; it is appended to Argv.
	Ask  i18n.Key
	Argv []string
}

// Firewall is the host table. `rollback` is missing on purpose: the timer runs it, and the command's own
// help says it is not for interactive use.
func Firewall() []Option {
	return []Option{
		{Label: i18n.SubFirewallStatusLabel, Hint: i18n.SubFirewallStatusHint, Argv: []string{"firewall", "status"}},
		{Label: i18n.SubFirewallOnLabel, Hint: i18n.SubFirewallOnHint, Argv: []string{"firewall", "on"}},
		{Label: i18n.SubFirewallOffLabel, Hint: i18n.SubFirewallOffHint, Argv: []string{"firewall", "off"}},
		{Label: i18n.SubFirewallConfirmLabel, Hint: i18n.SubFirewallConfirmHint, Argv: []string{"firewall", "confirm"}},
		{Label: i18n.SubFirewallRefreshLabel, Hint: i18n.SubFirewallRefreshHint, Argv: []string{"firewall", "refresh"}},
		{Label: i18n.SubFirewallRestoreLabel, Hint: i18n.SubFirewallRestoreHint, Argv: []string{"firewall", "restore"}},
	}
}

// Bans is what the guard banned and the operator's own hand. Only `block` is permanent, and only the
// operator can do it; the rest expire on their own.
func Bans() []Option {
	return []Option{
		{Label: i18n.SubBansListLabel, Hint: i18n.SubBansListHint, Argv: []string{"bans", "list"}},
		{Label: i18n.SubBansOffendersLabel, Hint: i18n.SubBansOffendersHint, Argv: []string{"bans", "offenders"}},
		{Label: i18n.SubBansBlockLabel, Hint: i18n.SubBansBlockHint, Ask: i18n.PromptAddress, Argv: []string{"bans", "block"}},
		{Label: i18n.SubBansUnbanLabel, Hint: i18n.SubBansUnbanHint, Ask: i18n.PromptAddress, Argv: []string{"bans", "unban"}},
		{Label: i18n.SubBansUnblockLabel, Hint: i18n.SubBansUnblockHint, Ask: i18n.PromptAddress, Argv: []string{"bans", "unblock"}},
	}
}

// Run turns one option into the command it names, asking the question the option declares. The choice
// itself belongs to the caller's menu: an option is a line, not a screen.
func (o Option) Run(ui *termui.UI, tr i18n.Translator) ([][]string, error) {
	argv := append([]string(nil), o.Argv...)
	if o.Ask != "" {
		value, err := ui.Required(tr.T(o.Ask), "")
		if err != nil {
			return nil, err
		}
		argv = append(argv, value)
	}
	return [][]string{argv}, nil
}
