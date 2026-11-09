package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/xray"
)

// HotUsers is the running core's client set, reached through the core's own management API. The
// interface exists so a test can watch a reconciliation without an Xray process.
type HotUsers interface {
	Users(ctx context.Context) ([]LiveUser, error)
	Add(ctx context.Context, clients []xray.Client) error
	Remove(ctx context.Context, emails []string) error
}

// LiveUser is one client the running core reports. The pair credential is only read for comparison
// and is never logged.
type LiveUser struct {
	Email string
	ID    string
}

// XrayAPI talks to the core through its own CLI (`xray api`). It is the same binary the node already
// pins, so no gRPC dependency is added to this module: the API is a documented command of the core we
// install, and the command validates its input with the core's own parser.
type XrayAPI struct {
	// Binary is the pinned core; Addr is its loopback API address.
	Binary string
	Addr   string
	// Tag is the VLESS inbound tag. Path is the XHTTP path nginx forwards.
	Tag  string
	Path string
	// Listen and Port describe the inbound the API command validates before it adds users.
	Listen  string
	Port    int
	WorkDir string
	Runner  Runner
}

// Users reads the running client set. An error means the API is not reachable, which is the signal to
// fall back to a validated restart rather than a verdict about the node.
func (a XrayAPI) Users(ctx context.Context) ([]LiveUser, error) {
	if a.Binary == "" || a.Runner == nil {
		return nil, errors.New("agent: the core API is not configured")
	}
	out, err := a.Runner.Run(ctx, a.Binary, "api", "inbounduser", "--server="+a.addr(), "-tag="+a.tag())
	if err != nil {
		return nil, fmt.Errorf("agent: read the running client set: %w", err)
	}
	var answer struct {
		Users []struct {
			Email   string `json:"email"`
			Account *struct {
				ID string `json:"id"`
			} `json:"account"`
		} `json:"users"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("agent: the core did not answer with a user list: %w", err)
	}
	users := make([]LiveUser, 0, len(answer.Users))
	for _, user := range answer.Users {
		if user.Email == "" {
			continue
		}
		live := LiveUser{Email: user.Email}
		if user.Account != nil {
			live.ID = strings.ToLower(user.Account.ID)
		}
		users = append(users, live)
	}
	return users, nil
}

// Add adds clients through the API. The command exits zero even when an individual user was refused,
// so the caller must verify with Users before it trusts the result.
func (a XrayAPI) Add(ctx context.Context, clients []xray.Client) error {
	if len(clients) == 0 {
		return nil
	}
	if a.Binary == "" || a.Runner == nil || a.Path == "" {
		return errors.New("agent: the core API is not configured")
	}
	payload := apiConfig{Inbounds: []apiInbound{{
		Tag:      a.tag(),
		Listen:   a.listen(),
		Port:     a.port(),
		Protocol: "vless",
		Settings: apiSettings{Clients: clients, Decryption: "none"},
		Stream: apiStream{
			Network:  "xhttp",
			Security: "none",
			XHTTP:    apiXHTTP{Path: a.Path, Mode: "packet-up"},
		},
	}}}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode the add-user request: %w", err)
	}
	path, err := a.stage(data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()

	if _, err := a.Runner.Run(ctx, a.Binary, "api", "adu", "--server="+a.addr(), path); err != nil {
		return fmt.Errorf("agent: add %d client(s) through the core API: %w", len(clients), err)
	}
	return nil
}

// Remove removes clients by email. Their email is the core's own identifier; the credential does not
// have to be known to remove it.
func (a XrayAPI) Remove(ctx context.Context, emails []string) error {
	if len(emails) == 0 {
		return nil
	}
	if a.Binary == "" || a.Runner == nil {
		return errors.New("agent: the core API is not configured")
	}
	args := append([]string{"api", "rmu", "--server=" + a.addr(), "-tag=" + a.tag()}, emails...)
	if _, err := a.Runner.Run(ctx, a.Binary, args...); err != nil {
		return fmt.Errorf("agent: remove %d client(s) through the core API: %w", len(emails), err)
	}
	return nil
}

// stage writes the API request outside the served tree: it carries credentials, so it is created
// 0600 and removed as soon as the command returns.
func (a XrayAPI) stage(data []byte) (string, error) {
	dir := a.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	file, err := os.CreateTemp(dir, ".adu-*.json")
	if err != nil {
		return "", fmt.Errorf("agent: stage the add-user request: %w", err)
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("agent: write the add-user request: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("agent: close the add-user request: %w", err)
	}
	return name, nil
}

func (a XrayAPI) addr() string {
	if a.Addr != "" {
		return a.Addr
	}
	return xray.DefaultListen + ":" + strconv.Itoa(xray.DefaultAPIPort)
}

func (a XrayAPI) tag() string {
	if a.Tag != "" {
		return a.Tag
	}
	return xray.InboundTag
}

func (a XrayAPI) listen() string {
	if a.Listen != "" {
		return a.Listen
	}
	return xray.DefaultListen
}

func (a XrayAPI) port() int {
	if a.Port > 0 {
		return a.Port
	}
	return xray.DefaultPort
}

// apiConfig is the shape `xray api adu` parses: a whole config with one inbound. The command builds
// the inbound before it adds its users, so listen and port have to be present.
type apiConfig struct {
	Inbounds []apiInbound `json:"inbounds"`
}

type apiInbound struct {
	Tag      string      `json:"tag"`
	Listen   string      `json:"listen"`
	Port     int         `json:"port"`
	Protocol string      `json:"protocol"`
	Settings apiSettings `json:"settings"`
	Stream   apiStream   `json:"streamSettings"`
}

type apiSettings struct {
	Clients    []xray.Client `json:"clients"`
	Decryption string        `json:"decryption"`
}

type apiStream struct {
	Network  string   `json:"network"`
	Security string   `json:"security"`
	XHTTP    apiXHTTP `json:"xhttpSettings"`
}

type apiXHTTP struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
}

// diffUsers compares the desired id→email set with what the core reports. A user whose email is
// right but whose credential differs is removed and re-added: the core keys users by email, so the
// old one has to go first.
func diffUsers(desired map[string]string, live []LiveUser) (add []xray.Client, remove []string) {
	byEmail := make(map[string]string, len(live))
	for _, user := range live {
		byEmail[user.Email] = strings.ToLower(user.ID)
	}
	wanted := make(map[string]bool, len(desired))
	for id, email := range desired {
		wanted[email] = true
		current, ok := byEmail[email]
		if !ok {
			add = append(add, xray.Client{ID: id, Email: email})
			continue
		}
		if current != "" && current != strings.ToLower(id) {
			remove = append(remove, email)
			add = append(add, xray.Client{ID: id, Email: email})
		}
	}
	for _, user := range live {
		if !wanted[user.Email] {
			remove = append(remove, user.Email)
		}
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Email < add[j].Email })
	sort.Strings(remove)
	return add, remove
}

// DiffUsersForTest exposes the reconciler's comparison to the tests. It is the same function Apply
// uses; the name says who may call it, not what it does.
func DiffUsersForTest(desired map[string]string, live []LiveUser) (add []xray.Client, remove []string) {
	return diffUsers(desired, live)
}

// matches reports whether the core reports exactly the desired set. Credentials are compared only
// when the core exposes them; an empty ID means the API did not decode the account.
func matches(desired map[string]string, live []LiveUser) bool {
	if len(desired) != len(live) {
		return false
	}
	wanted := make(map[string]string, len(desired))
	for id, email := range desired {
		wanted[email] = strings.ToLower(id)
	}
	seen := make(map[string]bool, len(live))
	for _, user := range live {
		id, ok := wanted[user.Email]
		if !ok || seen[user.Email] {
			return false
		}
		seen[user.Email] = true
		if user.ID != "" && user.ID != id {
			return false
		}
	}
	return true
}
