package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/hysteria"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

const (
	// hysteriaVersion is the pinned release of the QUIC transport.
	hysteriaVersion = "2.14.0"
	// hysteriaReleaseBase is a release directory. Hysteria's tags carry a path ("app/v2.14.0"), so the
	// base includes it and the layout below stays <base>/v<version>/<asset>.
	hysteriaReleaseBase = "https://github.com/apernet/hysteria/releases/download/app"
)

// hysteriaAssets pins the release asset and its digest per GOARCH, taken from the release's
// hashes.txt. A mirror cannot substitute a different binary because the digest is checked.
var hysteriaAssets = map[string]archivePin{
	"amd64": {Name: "hysteria-linux-amd64", SHA256: "fc25edff9948708cb00a4ff01f3bccde677660aca050133bb1ad46c6860362d2"},
	"arm64": {Name: "hysteria-linux-arm64", SHA256: "e7c52241299fe85aba11e9d36e85d8d88ddbb5799ac0e33d272219d20e313bab"},
}

// HysteriaHopFor is the core's loopback inbound for the transport, or nothing when it is off. The
// installer and the agent's sync both build a core document: a hop in one and not the other would make
// every transport request fail on the next sync.
func HysteriaHopFor(cfg *config.Config) xray.HysteriaHop {
	if cfg.Hysteria == nil {
		return xray.HysteriaHop{}
	}
	return xray.HysteriaHop{Enabled: true, Port: cfg.Hysteria.HopPort()}
}

// hysteriaPin is the asset and digest this architecture must install.
func hysteriaPin(arch string) (archivePin, error) {
	pin, ok := hysteriaAssets[arch]
	if !ok {
		return archivePin{}, fmt.Errorf("hysteria %s has no pinned binary for %s", hysteriaVersion, arch)
	}
	return pin, nil
}

// hysteriaBinStep installs the transport binary.
type hysteriaBinStep struct{ e *stepEnv }

func (*hysteriaBinStep) Name() string { return "hysteria-binary" }

func (s *hysteriaBinStep) Check(context.Context) (CheckResult, error) {
	if s.e.cfg.Hysteria == nil {
		return CheckResult{Satisfied: true, Detail: "the QUIC transport is not enabled"}, nil
	}
	pin, err := hysteriaPin(s.e.arch)
	if err != nil {
		return CheckResult{}, err
	}
	digest, err := fileSHA256(s.e.cfg.Paths.HysteriaBinary())
	if err != nil {
		return CheckResult{Detail: "the transport binary is missing"}, nil
	}
	if digest != pin.SHA256 {
		return CheckResult{Detail: "the transport binary does not match the pinned digest"}, nil
	}
	return CheckResult{Satisfied: true, Detail: "hysteria " + hysteriaVersion}, nil
}

func (s *hysteriaBinStep) Run(ctx context.Context) error {
	if s.e.cfg.Hysteria == nil {
		return nil
	}
	pin, err := hysteriaPin(s.e.arch)
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(s.e.cfg.Paths.StateDir, "cache")
	cached, err := fetchVerified(ctx, s.e.http, hysteriaReleaseBase, hysteriaVersion, pin, cacheDir, "hysteria")
	if err != nil {
		return err
	}
	s.e.st.AddPath(cacheDir, state.KindDir, "hysteria-binary", s.e.now())

	installed := s.e.cfg.Paths.HysteriaBinary()
	source, err := os.Open(cached) // #nosec G304 -- a path this step built.
	if err != nil {
		return fmt.Errorf("hysteria: open the verified download: %w", err)
	}
	defer func() { _ = source.Close() }()
	if err := copyVerified(installed, source, pin.SHA256, "hysteria"); err != nil {
		return err
	}
	if err := os.Chmod(installed, 0o755); err != nil {
		return fmt.Errorf("hysteria: chmod %s: %w", installed, err)
	}
	s.e.st.AddPath(installed, state.KindFile, "hysteria-binary", s.e.now())
	s.e.log.Info("installed the QUIC transport", "version", hysteriaVersion, "path", installed, "sha256", pin.SHA256)
	return nil
}

// hysteriaConfigStep writes the transport's configuration.
//
// Hysteria has no `-test`, unlike the core, so §11.3's "validate before it is applied" is kept
// differently: the document is ours and is decoded strictly before it is written, and a trial start on
// an ephemeral port proves the binary accepts it. Only then does the unit get the new file.
type hysteriaConfigStep struct{ e *stepEnv }

func (*hysteriaConfigStep) Name() string { return "hysteria-config" }

// TransportSpec builds the transport's document from the configuration and the pair UUIDs a node serves:
// its own device's and its peers'. Every user name and password is derived from the UUID, so this is the
// only place that mapping lives - the installer and the agent's sync both call it, and the server derives
// the same pair when it renders a participant's link.
func TransportSpec(cfg *config.Config, selfUUID string, peerUUIDs []string) hysteria.Spec {
	users := make(map[string]string, len(peerUUIDs)+1)
	if selfUUID != "" {
		users[proto.HysteriaUser(selfUUID)] = proto.HysteriaPassword(selfUUID)
	}
	for _, uuid := range peerUUIDs {
		if uuid == "" {
			continue
		}
		users[proto.HysteriaUser(uuid)] = proto.HysteriaPassword(uuid)
	}
	return hysteria.Spec{
		CertFile:      LeafCertPath(cfg),
		KeyFile:       filepath.Join(cfg.Paths.CertDir, keyFile),
		Users:         users,
		HopAddress:    fmt.Sprintf("127.0.0.1:%d", cfg.Hysteria.HopPort()),
		MasqueradeURL: "https://127.0.0.1:443",
	}
}

// spec is what the transport must be told: the owner's device, its local users and every peer of this node.
func (s *hysteriaConfigStep) spec() hysteria.Spec {
	uuids := make([]string, 0, len(s.e.st.Peers)+len(s.e.st.LocalUsers)+1)
	// The server's probe credential is a user like any other, and the only way it can check a node whose
	// transport is the QUIC one.
	if s.e.st.Credentials != nil && s.e.st.Credentials.ProbeUUID != "" {
		uuids = append(uuids, s.e.st.Credentials.ProbeUUID)
	}
	for _, user := range s.e.st.LocalUsers {
		uuids = append(uuids, user.ID)
	}
	for _, peer := range s.e.st.Peers {
		uuids = append(uuids, peer.UUID)
	}
	return TransportSpec(s.e.cfg, s.e.st.Identity.SelfUUID, uuids)
}

func (s *hysteriaConfigStep) Check(context.Context) (CheckResult, error) {
	if s.e.cfg.Hysteria == nil {
		return CheckResult{Satisfied: true, Detail: "the QUIC transport is not enabled"}, nil
	}
	want, err := hysteria.Generate(s.spec())
	if err != nil {
		return CheckResult{}, err
	}
	got, err := os.ReadFile(s.e.cfg.Paths.HysteriaConfig) // #nosec G304 -- a path from our own config.
	switch {
	case errors.Is(err, os.ErrNotExist):
		return CheckResult{Detail: s.e.cfg.Paths.HysteriaConfig + " does not exist"}, nil
	case err != nil:
		return CheckResult{}, fmt.Errorf("read %s: %w", s.e.cfg.Paths.HysteriaConfig, err)
	}
	if !bytes.Equal(got, want) {
		return CheckResult{Detail: "the transport config no longer matches the configuration"}, nil
	}
	info, err := os.Stat(s.e.cfg.Paths.HysteriaConfig)
	if err != nil {
		return CheckResult{}, fmt.Errorf("stat %s: %w", s.e.cfg.Paths.HysteriaConfig, err)
	}
	if chmodIsReal && info.Mode().Perm() != 0o640 {
		return CheckResult{Detail: fmt.Sprintf("%s has mode %04o, want 0640", s.e.cfg.Paths.HysteriaConfig, info.Mode().Perm())}, nil
	}
	return CheckResult{Satisfied: true, Detail: fmt.Sprintf("%d user(s) through %s",
		len(s.spec().Users), s.spec().HopAddress)}, nil
}

func (s *hysteriaConfigStep) Run(ctx context.Context) error {
	if s.e.cfg.Hysteria == nil {
		return nil
	}
	data, err := hysteria.Generate(s.spec())
	if err != nil {
		return err
	}
	// Our own document, decoded strictly: a marshalling bug must not reach a running transport.
	if err := hysteria.Decode(data); err != nil {
		return fmt.Errorf("the generated transport config does not read back: %w", err)
	}
	if err := s.trialStart(ctx, data); err != nil {
		return err
	}

	target := s.e.cfg.Paths.HysteriaConfig
	previous, previousErr := os.ReadFile(target) // #nosec G304 -- a path from our own config.
	staged := target + ".new"
	if err := os.WriteFile(staged, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", staged, err)
	}
	defer func() { _ = os.Remove(staged) }()
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "chown", Args: []string{"root:" + transportUser, staged}}); err != nil {
		return fmt.Errorf("give %s to root:%s: %w", staged, transportUser, err)
	}
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("install %s: %w", target, err)
	}
	now := s.e.now()
	s.e.st.AddPath(target, state.KindFile, "hysteria-config", now)
	s.e.st.Touch()

	if !s.e.serviceActive(ctx, service.HysteriaUnitName) {
		return nil
	}
	_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reset-failed", service.HysteriaUnitName}})
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.HysteriaUnitName}}); err != nil {
		if previousErr == nil {
			if rollbackErr := os.WriteFile(target, previous, 0o640); rollbackErr == nil {
				_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.HysteriaUnitName}})
				return fmt.Errorf("restart %s: %w (the previous config was restored)", service.HysteriaUnitName, err)
			}
		}
		return fmt.Errorf("restart %s: %w", service.HysteriaUnitName, err)
	}
	return nil
}

// trialStart runs the binary on an ephemeral loopback port and lets it be killed. Hysteria reads its
// whole configuration before it listens, so a rejected file fails immediately; a file it accepts keeps
// running until `timeout` ends it. There is no `-test` to call instead.
func (s *hysteriaConfigStep) trialStart(ctx context.Context, data []byte) error {
	spec := s.spec()
	spec.Listen = "127.0.0.1:0"
	trial, err := hysteria.Generate(spec)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.e.cfg.Paths.HysteriaConfig), ".hysteria-trial*.yaml")
	if err != nil {
		return fmt.Errorf("hysteria: trial config: %w", err)
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := file.Write(trial); err != nil {
		_ = file.Close()
		return fmt.Errorf("hysteria: trial config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("hysteria: trial config: %w", err)
	}
	_, err = s.e.exec.Run(ctx, Cmd{
		Name: "timeout",
		Args: []string{"2", s.e.cfg.Paths.HysteriaBinary(), "server", "-c", name},
	})
	switch {
	case err == nil, exitCode(err) == 124:
		return nil
	default:
		return fmt.Errorf("the generated transport config was rejected by the binary: %w", err)
	}
}
