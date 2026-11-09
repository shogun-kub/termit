package install

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// serviceUser owns the proxy core. It exists so Xray does not run as root: it listens on a loopback
// port and needs no capability.
const serviceUser = "termit"

// aptEnv keeps a package install from asking anything.
var aptEnv = []string{
	"DEBIAN_FRONTEND=noninteractive",
	"APT_LISTCHANGES_FRONTEND=none",
}

// requiredPackages are the only packages Termit installs. nginx terminates TLS and serves both the
// decoy and the proxy path; ca-certificates lets ACME verify the CA's own chain.
var requiredPackages = []string{"nginx", "ca-certificates"}

// aptRefresh is how long an apt index is considered current before `uv init` updates it again.
const aptRefresh = 6 * time.Hour

// stepEnv is the shared state every step works with.
type stepEnv struct {
	cfg       *config.Config
	st        *state.State
	statePath string
	exec      Executor
	http      *http.Client
	log       *slog.Logger
	now       func() time.Time
	arch      string
	// configPath is the file this run was started with; executable is the binary the units run.
	configPath string
	executable string
	// forced is true while a step that the run explicitly forced is executing. A step whose Run is
	// not idempotent (renewing a certificate) needs to know the difference between "converge" and
	// "do it again".
	forced bool
}

// binaryPath is the Termit binary the generated units run. It defaults to the running executable, so
// a node renews its certificate with the same build that installed it.
func (e *stepEnv) binaryPath() (string, error) {
	if e.executable != "" {
		return e.executable, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	return path, nil
}

// saveState persists the state if a step changed it.
func (e *stepEnv) saveState() error {
	if !e.st.Dirty() {
		return nil
	}
	return e.st.SaveAt(e.statePath, e.now())
}

// dirs are the directories the agent owns, with the mode each must have.
type dir struct {
	path string
	mode os.FileMode
}

func (e *stepEnv) dirs() []dir {
	return []dir{
		{e.cfg.Paths.ConfigDir, 0o750},
		{e.cfg.Paths.StateDir, 0o750},
		{e.cfg.Paths.CertDir, 0o700},
		{e.cfg.Paths.ACMEWebroot, 0o755},
		{e.cfg.Paths.SiteDir, 0o755},
		{e.cfg.Paths.BinDir, 0o755},
	}
}

// preflightStep runs the host checks before anything is written. It never reports itself as
// satisfied: the host can change between runs, and the checks are cheap.
type preflightStep struct{ e *stepEnv }

func (*preflightStep) Name() string { return "preflight" }

func (*preflightStep) Check(context.Context) (CheckResult, error) {
	return CheckResult{Detail: "host checks run on every install"}, nil
}

func (s *preflightStep) Run(ctx context.Context) error {
	report := RunPreflight(ctx, PreflightOptions{
		Config: s.e.cfg,
		Exec:   s.e.exec,
		HTTP:   s.e.http,
		Arch:   s.e.arch,
		Now:    s.e.now,
	})
	for _, warning := range report.Warnings() {
		s.e.log.Debug("preflight warning", "check", warning.Name, "detail", warning.Detail, "hint", warning.Hint)
	}
	failures := report.Failures()
	if len(failures) == 0 {
		return nil
	}
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		part := failure.Name + ": " + failure.Detail
		if failure.Hint != "" {
			part += " (" + failure.Hint + ")"
		}
		parts = append(parts, part)
	}
	return fmt.Errorf("preflight failed: %s", strings.Join(parts, "; "))
}

// userStep creates the unprivileged account the proxy core runs as.
type userStep struct{ e *stepEnv }

func (*userStep) Name() string { return "user" }

func (s *userStep) Check(ctx context.Context) (CheckResult, error) {
	_, err := s.e.exec.Run(ctx, Cmd{Name: "id", Args: []string{"-u", serviceUser}})
	if err == nil {
		return CheckResult{Satisfied: true, Detail: "system user " + serviceUser}, nil
	}
	if exitCode(err) == 1 || notFound(err) {
		return CheckResult{Detail: "system user " + serviceUser + " does not exist"}, nil
	}
	return CheckResult{}, fmt.Errorf("query user %s: %w", serviceUser, err)
}

func (s *userStep) Run(ctx context.Context) error {
	_, err := s.e.exec.Run(ctx, Cmd{Name: "useradd", Args: []string{
		"--system",
		"--no-create-home",
		"--home-dir", "/nonexistent",
		"--shell", "/usr/sbin/nologin",
		"--user-group",
		serviceUser,
	}})
	if err != nil {
		return fmt.Errorf("create user %s: %w", serviceUser, err)
	}
	s.e.st.AddPath(serviceUser, state.KindUser, "user", s.e.now())
	return nil
}

// dirsStep creates the agent's directories with the modes the safety rules require.
type dirsStep struct{ e *stepEnv }

func (*dirsStep) Name() string { return "dirs" }

func (s *dirsStep) Check(ctx context.Context) (CheckResult, error) {
	var missing []string
	for _, d := range s.e.dirs() {
		info, err := os.Stat(d.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			missing = append(missing, d.path)
			continue
		case err != nil:
			return CheckResult{}, fmt.Errorf("stat %s: %w", d.path, err)
		case !info.IsDir():
			return CheckResult{}, fmt.Errorf("%s exists but is not a directory", d.path)
		}
		// Ownership is part of the effect: a directory that exists but does not belong to the
		// service user's group keeps the proxy core from reading its configuration.
		if d.path == s.e.cfg.Paths.ConfigDir {
			if group, ok := s.groupOwner(ctx, d.path); ok && group != serviceUser {
				missing = append(missing, d.path+" belongs to group "+group)
			}
		}
	}
	if len(missing) > 0 {
		return CheckResult{Detail: "missing: " + strings.Join(missing, ", ")}, nil
	}
	return CheckResult{Satisfied: true, Detail: "all directories exist"}, nil
}

func (s *dirsStep) Run(ctx context.Context) error {
	for _, d := range s.e.dirs() {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("create %s: %w", d.path, err)
		}
		// MkdirAll applies the process umask, so the mode is set explicitly afterwards.
		if err := os.Chmod(d.path, d.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", d.path, err)
		}
		if d.path == s.e.cfg.Paths.ConfigDir {
			if err := s.allowServiceUser(ctx, d.path); err != nil {
				return err
			}
		}
		s.e.st.AddPath(d.path, state.KindDir, "dirs", s.e.now())
	}
	return nil
}

// groupOwner reports the group of a path using stat(1), so the check needs no platform code. The
// second result is false when the tool cannot answer, which must not turn into a false failure.
func (s *dirsStep) groupOwner(ctx context.Context, path string) (string, bool) {
	res, err := s.e.exec.Run(ctx, Cmd{Name: "stat", Args: []string{"-c", "%G", path}})
	if err != nil {
		return "", false
	}
	group := strings.TrimSpace(res.Stdout)
	return group, group != ""
}

// allowServiceUser lets the proxy core read its own configuration. It runs as an unprivileged user,
// so the directory belongs to that user's group and stays 0750: the config is readable by the core
// and by root, by nobody else. Its parent has to be traversable, and that parent is ours.
func (s *dirsStep) allowServiceUser(ctx context.Context, dir string) error {
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "termit" {
		if err := os.Chmod(parent, 0o755); err != nil {
			return fmt.Errorf("chmod %s: %w", parent, err)
		}
	}
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "chown", Args: []string{"root:" + serviceUser, dir}}); err != nil {
		return fmt.Errorf("give %s to root:%s: %w", dir, serviceUser, err)
	}
	return nil
}

// packagesStep installs nginx and the CA bundle through apt.
type packagesStep struct{ e *stepEnv }

func (*packagesStep) Name() string { return "packages" }

func (s *packagesStep) Check(ctx context.Context) (CheckResult, error) {
	for _, pkg := range requiredPackages {
		installed, err := dpkgInstalled(ctx, s.e.exec, pkg)
		if err != nil {
			return CheckResult{}, err
		}
		if !installed {
			return CheckResult{Detail: pkg + " is not installed"}, nil
		}
	}
	return CheckResult{Satisfied: true, Detail: strings.Join(requiredPackages, ", ") + " are installed"}, nil
}

func (s *packagesStep) Run(ctx context.Context) error {
	stale, err := s.e.aptListsStale()
	if err != nil {
		return err
	}
	if stale {
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "apt-get", Args: []string{"update", "-q"}, Env: aptEnv}); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}
		if err := s.e.markAptUpdated(); err != nil {
			return err
		}
	}

	args := []string{
		"--yes", "--quiet", "--no-install-recommends",
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
		"install",
	}
	args = append(args, requiredPackages...)
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "apt-get", Args: args, Env: aptEnv}); err != nil {
		return fmt.Errorf("apt-get install %s: %w", strings.Join(requiredPackages, " "), err)
	}
	return nil
}

// aptListsStale reports whether the package index should be refreshed.
func (e *stepEnv) aptListsStale() (bool, error) {
	info, err := os.Stat(e.aptStampPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("stat %s: %w", e.aptStampPath(), err)
	}
	return e.now().Sub(info.ModTime()) > aptRefresh, nil
}

func (e *stepEnv) aptStampPath() string { return filepath.Join(e.cfg.Paths.StateDir, "apt-updated") }

func (e *stepEnv) markAptUpdated() error {
	if err := state.WriteFileAtomic(e.aptStampPath(), []byte(e.now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write apt stamp: %w", err)
	}
	return nil
}

// xrayStep installs the pinned proxy core.
type xrayStep struct{ e *stepEnv }

func (*xrayStep) Name() string { return "xray-binary" }

func (s *xrayStep) Check(ctx context.Context) (CheckResult, error) {
	binary := filepath.Clean(s.e.cfg.Paths.XrayBinary())
	if filepath.Clean(s.e.st.Xray.Path) != binary || s.e.st.Xray.Version != s.e.cfg.Xray.Version || s.e.st.Xray.SHA256 == "" {
		return CheckResult{Detail: "no installed core recorded in state"}, nil
	}
	digest, err := fileSHA256(binary)
	if err != nil {
		return CheckResult{Detail: "installed core is unreadable or missing"}, nil
	}
	if digest != s.e.st.Xray.SHA256 {
		return CheckResult{Detail: "installed core does not match the recorded digest"}, nil
	}
	version, err := binaryVersion(ctx, s.e.exec, binary)
	if err != nil {
		return CheckResult{Detail: "installed core does not run: " + err.Error()}, nil
	}
	if version != s.e.cfg.Xray.Version {
		return CheckResult{Detail: fmt.Sprintf("installed core reports %s, want %s", version, s.e.cfg.Xray.Version)}, nil
	}
	return CheckResult{Satisfied: true, Detail: "xray " + version + " at " + binary}, nil
}

func (s *xrayStep) Run(ctx context.Context) error {
	cacheDir := filepath.Join(s.e.cfg.Paths.StateDir, "cache")
	archivePath, err := fetchArchive(ctx, s.e.http, s.e.cfg, s.e.arch, cacheDir)
	if err != nil {
		return err
	}
	s.e.st.AddPath(cacheDir, state.KindDir, "xray-binary", s.e.now())

	installed, err := extractXray(archivePath, s.e.cfg.Paths.BinDir)
	if err != nil {
		return err
	}
	version, err := binaryVersion(ctx, s.e.exec, installed)
	if err != nil {
		_ = os.Remove(installed)
		return err
	}
	if version != s.e.cfg.Xray.Version {
		_ = os.Remove(installed)
		return fmt.Errorf("xray: installed core reports %s, expected %s", version, s.e.cfg.Xray.Version)
	}
	digest, err := fileSHA256(installed)
	if err != nil {
		return err
	}
	s.e.st.Xray = state.Xray{Version: version, SHA256: digest, Path: filepath.Clean(installed)}
	s.e.st.AddPath(filepath.Clean(installed), state.KindFile, "xray-binary", s.e.now())
	s.e.log.Info("installed proxy core", "version", version, "path", installed, "sha256", digest)
	return nil
}

// identityStep generates the node's own secrets once and never again.
type identityStep struct{ e *stepEnv }

func (*identityStep) Name() string { return "identity" }

func (s *identityStep) Check(context.Context) (CheckResult, error) {
	switch {
	case !s.e.st.Installed():
		return CheckResult{Detail: "identity has not been generated"}, nil
	case s.e.st.Decoy.Seed == "":
		return CheckResult{Detail: "decoy seed has not been generated"}, nil
	case s.e.st.Decoy.Template != s.e.cfg.Decoy.Template:
		return CheckResult{Detail: fmt.Sprintf("decoy template is %q, configuration says %q",
			s.e.st.Decoy.Template, s.e.cfg.Decoy.Template)}, nil
	}
	return CheckResult{Satisfied: true, Detail: "identity and decoy seed are present"}, nil
}

func (s *identityStep) Run(context.Context) error {
	if s.e.st.Identity.SelfUUID == "" {
		uuid, err := NewUUID()
		if err != nil {
			return err
		}
		s.e.st.Identity.SelfUUID = uuid
		s.e.log.Info("generated node identity", "fingerprint", state.Fingerprint(uuid))
	}
	if s.e.st.Identity.XHTTPPath == "" {
		path, err := randomHex(16)
		if err != nil {
			return err
		}
		s.e.st.Identity.XHTTPPath = path
		s.e.log.Info("generated proxy path", "fingerprint", state.Fingerprint(path))
	}
	if s.e.st.Decoy.Seed == "" {
		seed, err := randomHex(16)
		if err != nil {
			return err
		}
		s.e.st.Decoy.Seed = seed
		s.e.log.Info("generated decoy seed", "fingerprint", state.Fingerprint(seed))
	}
	if current := s.e.st.Decoy.Template; current != s.e.cfg.Decoy.Template {
		s.e.st.Decoy.Template = s.e.cfg.Decoy.Template
		if current == "" {
			s.e.log.Debug("decoy template recorded", "template", s.e.cfg.Decoy.Template)
		} else {
			s.e.log.Info("decoy template changed; run `uv renew-site` to regenerate the site",
				"from", current, "to", s.e.cfg.Decoy.Template)
		}
	}
	s.e.st.Touch()
	return nil
}

// dpkgInstalled reports whether a package is in the "install ok installed" state.
func dpkgInstalled(ctx context.Context, exec Executor, pkg string) (bool, error) {
	res, err := exec.Run(ctx, Cmd{Name: "dpkg-query", Args: []string{"-W", "-f=${Status}", pkg}})
	if err != nil {
		switch {
		case notFound(err):
			return false, errors.New("dpkg-query is not available; this is not a Debian host")
		case exitCode(err) == 1:
			return false, nil // dpkg-query exits 1 for an unknown package
		default:
			return false, fmt.Errorf("query package %s: %w", pkg, err)
		}
	}
	return strings.TrimSpace(res.Stdout) == "install ok installed", nil
}
