package install

import (
	"context"
	"errors"
	"fmt"
	"github.com/shogun-kub/termit/internal/uv/service"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// unitNames are the services Termit supervises. They are listed here as well as in the manifest so
// uninstall stops them even when an older install never recorded them; the constants are the ones the
// generator wrote, so a rename cannot leave a running process behind.
var unitNames = []string{
	service.XrayUnitName,
	service.AgentUnitName,
	service.RenewTimerName,
	service.RenewUnitName,
}

// UninstallOptions configure an uninstall.
type UninstallOptions struct {
	Config *config.Config
	Exec   Executor
	Logger *slog.Logger
	Now    func() time.Time
	// Purge also removes the configuration, state, certificates and the service user. Without it
	// those survive, so a reinstall keeps the same identity.
	Purge bool
	// DryRun reports what would be removed without changing anything.
	DryRun bool
}

// UninstallReport lists what was removed and what was deliberately kept.
type UninstallReport struct {
	Removed  []string
	Kept     []string
	Warnings []string
}

// Uninstall removes what Termit created and nothing else: the units, the installed core, the
// generated nginx vhost, and with Purge the configuration, state, certificates and service user.
//
// It never removes packages. nginx may serve other sites on this host, and a proxy that deletes the
// host's web server on the way out is not one anybody should install.
func Uninstall(ctx context.Context, opts UninstallOptions) (*UninstallReport, error) {
	if opts.Config == nil {
		return nil, errors.New("uninstall: a configuration is required")
	}
	if opts.Exec == nil {
		opts.Exec = OSExecutor{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := opts.Config
	report := &UninstallReport{}

	st, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		report.Warnings = append(report.Warnings, "no state file: Termit does not appear to be installed")
		return report, nil
	}
	if err != nil {
		return nil, err
	}

	// 0. The tunnel exists only to carry this node's traffic. Leaving it connected after the node is
	// gone would be a VPN nobody asked for; the package itself stays, like every other package.
	client := warp.New(warpRunner{exec: opts.Exec})
	if client.Installed(ctx) {
		if opts.DryRun {
			report.Removed = append(report.Removed, "the WARP tunnel (disconnect)")
		} else if err := client.Disconnect(ctx); err != nil {
			report.Warnings = append(report.Warnings, "disconnect WARP: "+err.Error())
		} else {
			report.Removed = append(report.Removed, "the WARP tunnel (disconnected)")
		}
		report.Kept = append(report.Kept, "cloudflare-warp (remove it with `apt-get remove cloudflare-warp`)")
	}

	// 1. Stop the services before their binaries disappear.
	if !opts.DryRun {
		args := append([]string{"disable", "--now"}, unitNames...)
		if _, err := opts.Exec.Run(ctx, Cmd{Name: "systemctl", Args: args}); err != nil && !notFound(err) {
			// Units that were never installed make systemctl exit non-zero; that is not a problem.
			opts.Logger.Debug("systemctl disable", "error", err)
		}
	}

	// 2. Three passes over the manifest: the things that hold bytes, then the user, then the
	// directories that held them. A directory cannot go before what is inside it, and the manifest is
	// written in creation order, so one pass can only work by accident.
	managed := func(path string) bool {
		return isUnder(path, cfg.Paths.ConfigDir) || isUnder(path, cfg.Paths.StateDir)
	}
	skip := func(entry state.Path) bool {
		return entry.Path == cfg.Paths.NginxSite // removed below, together with the nginx reload
	}

	for i := len(st.Manifest) - 1; i >= 0; i-- {
		entry := st.Manifest[i]
		if entry.Kind == state.KindDir || entry.Kind == state.KindUser || skip(entry) {
			continue
		}
		if !opts.Purge && managed(entry.Path) {
			report.Kept = append(report.Kept, entry.Path)
			continue
		}
		report.removeFile(entry.Path, opts.DryRun)
	}

	for i := len(st.Manifest) - 1; i >= 0; i-- {
		entry := st.Manifest[i]
		if entry.Kind != state.KindUser {
			continue
		}
		if !opts.Purge {
			report.Kept = append(report.Kept, "user "+entry.Path)
			continue
		}
		if opts.DryRun {
			report.Removed = append(report.Removed, "user "+entry.Path)
			continue
		}
		if _, err := opts.Exec.Run(ctx, Cmd{Name: "userdel", Args: []string{entry.Path}}); err != nil && exitCode(err) != 6 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("remove user %s: %v", entry.Path, err))
		} else {
			report.Removed = append(report.Removed, "user "+entry.Path)
		}
	}

	// Deepest first, so a directory is empty by the time its turn comes.
	dirs := make([]string, 0, len(st.Manifest))
	for _, entry := range st.Manifest {
		if entry.Kind != state.KindDir || skip(entry) {
			continue
		}
		dirs = append(dirs, entry.Path)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if managed(dir) {
			if !opts.Purge {
				report.Kept = append(report.Kept, dir)
			}
			continue // a purged tree goes as a whole below
		}
		report.removeDir(dir, opts.DryRun)
	}

	// 3. Debian's own vhost goes back before the vhost directory is touched. It was moved aside so
	// that a request to the bare address answers the decoy instead of the welcome page.
	report.restoreStockSite(cfg, opts.DryRun)

	// 4. The nginx vhost goes with its own reload, so :443 and :80 are released.
	nginxTouched := st.HasPath(cfg.Paths.NginxSite)
	if nginxTouched {
		report.removeFile(cfg.Paths.NginxSite, opts.DryRun)
		if !opts.DryRun {
			if _, err := opts.Exec.Run(ctx, Cmd{Name: "nginx", Args: []string{"-t"}}); err != nil {
				report.Warnings = append(report.Warnings, "nginx -t failed after removing the vhost: "+err.Error())
			} else if _, err := opts.Exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reload", "nginx"}}); err != nil {
				report.Warnings = append(report.Warnings, "reload nginx: "+err.Error())
			}
		}
	}

	// 5. Purge the two trees Termit owns, and only those.
	if opts.Purge {
		for _, dir := range []string{cfg.Paths.StateDir, cfg.Paths.ConfigDir} {
			if !safeToRemove(dir) {
				report.Warnings = append(report.Warnings, "refusing to purge "+dir+": not a Termit-owned path")
				continue
			}
			if opts.DryRun {
				report.Removed = append(report.Removed, dir+" (tree)")
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				return report, fmt.Errorf("uninstall: remove %s: %w", dir, err)
			}
			report.Removed = append(report.Removed, dir+" (tree)")
		}
		// The parent (/etc/termit, /var/lib/termit, /usr/lib/termit) is ours too when it is empty.
		for _, parent := range parentsToRemove(cfg.Paths.StateDir, cfg.Paths.ConfigDir, cfg.Paths.BinDir, cfg.Paths.SiteDir, cfg.Paths.ACMEWebroot) {
			if opts.DryRun {
				report.Removed = append(report.Removed, parent)
				continue
			}
			if err := os.Remove(parent); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					report.Warnings = append(report.Warnings, fmt.Sprintf("kept %s: %v", parent, err))
				}
				continue
			}
			report.Removed = append(report.Removed, parent)
		}
	} else {
		report.Kept = append(report.Kept, cfg.Paths.ConfigDir, cfg.Paths.StateDir)
	}

	return report, nil
}

// restoreStockSite puts the distribution's default vhost back under sites-enabled, but only if the
// web step moved it and nothing has taken its place.
func (r *UninstallReport) restoreStockSite(cfg *config.Config, dryRun bool) {
	parked := filepath.Join(filepath.Dir(cfg.Paths.NginxSite), stockSite+disabledBy)
	if _, err := os.Lstat(parked); err != nil {
		return
	}
	enabled := filepath.Join(filepath.Dir(cfg.Paths.SitesEnabled), stockSite)
	if _, err := os.Lstat(enabled); err == nil {
		r.Kept = append(r.Kept, "the stock nginx vhost is still in place at "+enabled)
		return
	}
	if dryRun {
		r.Removed = append(r.Removed, parked+" (would be restored to "+enabled+")")
		return
	}
	if err := os.Rename(parked, enabled); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("restore the stock nginx vhost: %v", err))
		return
	}
	r.Removed = append(r.Removed, parked+" (restored to "+enabled+")")
}

func (r *UninstallReport) removeFile(target string, dryRun bool) {
	if dryRun {
		r.Removed = append(r.Removed, target)
		return
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("remove %s: %v", target, err))
		return
	}
	r.Removed = append(r.Removed, target)
}

// removeDir removes a directory only when it is empty: a directory that still holds something the
// manifest does not know about is left alone and reported.
func (r *UninstallReport) removeDir(target string, dryRun bool) {
	if dryRun {
		r.Removed = append(r.Removed, target)
		return
	}
	err := os.Remove(target)
	switch {
	case err == nil:
		r.Removed = append(r.Removed, target)
	case errors.Is(err, os.ErrNotExist):
		r.Removed = append(r.Removed, target)
	default:
		r.Warnings = append(r.Warnings, fmt.Sprintf("kept %s: %v", target, err))
	}
}

// parentsToRemove returns the distinct parents of the two trees, in a stable order.
func parentsToRemove(dirs ...string) []string {
	seen := make(map[string]bool, len(dirs))
	var out []string
	for _, dir := range dirs {
		parent := path.Dir(path.Clean(dir))
		if seen[parent] || !safeParentToRemove(parent) {
			continue
		}
		seen[parent] = true
		out = append(out, parent)
	}
	return out
}

// safeParentToRemove allows removing a directory only when it is the Termit-owned parent itself.
func safeParentToRemove(parent string) bool {
	clean := path.Clean(parent)
	if path.Base(clean) != "termit" {
		return false
	}
	switch path.Dir(clean) {
	case "/etc", "/var", "/var/lib", "/usr", "/usr/lib", "/var/www", "/opt", "/srv":
		return true
	}
	return false
}

// isUnder reports whether target is root or inside it.
func isUnder(target, root string) bool {
	cleanTarget := path.Clean(target)
	cleanRoot := path.Clean(root)
	return cleanTarget == cleanRoot || strings.HasPrefix(cleanTarget, cleanRoot+"/")
}

// safeToRemove refuses the paths a corrupted manifest could otherwise turn into a catastrophe.
func safeToRemove(target string) bool {
	clean := path.Clean(target)
	if clean == "/" {
		return false
	}
	switch clean {
	case "/etc", "/var", "/usr", "/home", "/root", "/tmp", "/opt", "/srv", "/var/www":
		return false
	}
	// A volume root such as `C:\` has no depth to speak of.
	if strings.HasSuffix(clean, `:\`) || strings.HasSuffix(clean, ":/") {
		return false
	}
	// At least "…/termit/uv": never a top-level or single-child directory.
	depth := strings.Count(clean, "/") + strings.Count(clean, `\`)
	return depth >= 3
}
