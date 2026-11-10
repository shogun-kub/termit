package warp

import (
	"context"
	"fmt"
	"time"
)

// Package is the Debian package this script installs. The install step declares it through the
// install package's dependency list.
const Package = "cloudflare-warp"

// InstallScript prepares Cloudflare's apt repository and installs the client. The repository is
// signed, so apt verifies every package; there is no piped-to-shell download of a binary.
//
// Cloudflare publishes a suite per Debian release (trixie and bookworm are both there today). The
// fallback covers the window between a Debian release and Cloudflare adding its suite, which is
// exactly the kind of thing that would otherwise fail on a fresh machine months from now.
const InstallScript = `set -eu
keyring=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg
list=/etc/apt/sources.list.d/cloudflare-client.list
install -d -m 0755 /usr/share/keyrings
curl -fsSL --retry 3 --connect-timeout 15 https://pkg.cloudflareclient.com/pubkey.gpg -o "$keyring"
chmod 0644 "$keyring"
. /etc/os-release
suite="${VERSION_CODENAME:-bookworm}"
write_list() {
  printf 'deb [signed-by=%s] https://pkg.cloudflareclient.com/ %s main\n' "$keyring" "$1" > "$list"
}
write_list "$suite"
if ! DEBIAN_FRONTEND=noninteractive apt-get update -qq; then
  write_list bookworm
  DEBIAN_FRONTEND=noninteractive apt-get update -qq
fi
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends cloudflare-warp
`

// Installed reports whether warp-cli answers on this host.
func (c Client) Installed(ctx context.Context) bool {
	_, err := c.exec(ctx, c.binary(), "--version")
	return err == nil
}

// Registered reports whether this device already has a WARP registration. Registration mints an
// account; running it again would replace one for no reason.
func (c Client) Registered(ctx context.Context) bool {
	_, err := c.cli(ctx, "--accept-tos", "registration", "show")
	return err == nil
}

// Ensure converges the sidecar: the package is installed, the device is registered, the client is in
// proxy mode and connected. Every step first asks whether its effect is already there, so `uv init`
// can run it on a fresh host and on a converged one.
func (c Client) Ensure(ctx context.Context) error {
	if err := c.Host.Check(); err != nil {
		return err
	}
	if !c.Installed(ctx) {
		if _, err := c.exec(ctx, "sh", "-c", InstallScript); err != nil {
			return fmt.Errorf("install %s: %w", Package, err)
		}
		if !c.Installed(ctx) {
			return fmt.Errorf("%s was installed but warp-cli still does not run", Package)
		}
	}
	if !c.Registered(ctx) {
		if _, err := c.cli(ctx, "--accept-tos", "registration", "new"); err != nil {
			return fmt.Errorf("register this device: %w", err)
		}
	}
	// Proxy mode is the whole reason this package exists here: it keeps WARP away from host traffic.
	if _, err := c.cli(ctx, "--accept-tos", "mode", "proxy"); err != nil {
		return fmt.Errorf("switch warp-cli to proxy mode: %w", err)
	}
	if _, err := c.cli(ctx, "--accept-tos", "connect"); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return c.waitConnected(ctx)
}

// Disconnect stops the tunnel. A registered client stays registered, so turning WARP back on does
// not mint a new account.
func (c Client) Disconnect(ctx context.Context) error {
	if !c.Installed(ctx) {
		return nil
	}
	if _, err := c.cli(ctx, "--accept-tos", "disconnect"); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	return nil
}

func (c Client) waitConnected(ctx context.Context) error {
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = ConnectAttempts
	}
	interval := c.Interval
	if interval <= 0 {
		interval = ConnectInterval
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = Sleep
	}
	var last string
	for attempt := 0; attempt < attempts; attempt++ {
		out, err := c.cli(ctx, "--accept-tos", "status")
		last = out
		if err == nil && parseState(out) == "connected" {
			return nil
		}
		if attempt == attempts-1 {
			break
		}
		if err := sleep(ctx, interval); err != nil {
			return err
		}
	}
	return fmt.Errorf("the client did not connect within %s: %s", time.Duration(attempts)*interval, last)
}

// Remove stops the client and removes the package. It is what `uv uninstall --purge` needs: the
// tunnel's only purpose was this node's proxy, and leaving a connected VPN behind after the node is
// gone would be a surprise.
func (c Client) Remove(ctx context.Context) error {
	if !c.Installed(ctx) {
		return nil
	}
	_ = c.Disconnect(ctx)
	if _, err := c.exec(ctx, "apt-get", "remove", "-y", "-qq", "--purge", "cloudflare-warp"); err != nil {
		return fmt.Errorf("remove cloudflare-warp: %w", err)
	}
	return nil
}
