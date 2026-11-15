package install

import (
	"context"
	"fmt"

	"github.com/shogun-kub/termit/internal/uv/service"
)

// nginxUnit is the distribution's unit. Termit never replaces it; the hardening lives in a drop-in.
const nginxUnit = "nginx.service"

// startStep brings the services up and proves they are up.
//
// "The unit is active" is not "the node works", so this step also asks the core to list its own
// inbounds through the API: that answer only exists when the config was accepted and the handler is
// running. The end-to-end check through nginx belongs to the acceptance suite.
type startStep struct{ e *stepEnv }

func (*startStep) Name() string { return "start" }

func (s *startStep) Check(ctx context.Context) (CheckResult, error) {
	if !s.e.serviceActive(ctx, service.XrayUnitName) {
		return CheckResult{Detail: service.XrayUnitName + " is not running"}, nil
	}
	if !s.e.serviceActive(ctx, nginxUnit) {
		return CheckResult{Detail: nginxUnit + " is not running"}, nil
	}
	if !s.e.serviceActive(ctx, service.RenewTimerName) {
		return CheckResult{Detail: service.RenewTimerName + " is not running"}, nil
	}
	// The agent is restarted even when it is running: `termit-uv init` is also the upgrade path, and a unit
	// file or the binary may have been replaced under the running process. Reporting "present" here
	// would leave an old agent in place until the next reboot.
	return CheckResult{Detail: "the agent is restarted so a new binary or unit takes effect"}, nil
}

func (s *startStep) Run(ctx context.Context) error {
	for _, unit := range []string{service.XrayUnitName, nginxUnit, service.RenewTimerName} {
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"enable", "--now", unit}}); err != nil {
			return fmt.Errorf("enable %s: %w", unit, err)
		}
		if !s.e.serviceActive(ctx, unit) {
			return fmt.Errorf("%s did not stay running", unit)
		}
	}

	// The API answers only when the core parsed the config and started its handler. This runs before
	// the agent is up, so the check cannot race a peer sync that restarts the core.
	spec, err := (&xrayConfigStep{s.e}).spec()
	if err != nil {
		return err
	}
	if _, err := s.e.exec.Run(ctx, Cmd{
		Name: s.e.cfg.Paths.XrayBinary(),
		Args: []string{"api", "lsi", "--server=" + spec.APIAddress()},
	}); err != nil {
		return fmt.Errorf("the proxy core is running but does not answer on %s: %w", spec.APIAddress(), err)
	}

	// The agent starts last. `enable --now` leaves an already-running unit on the old binary after an
	// upgrade, so it is restarted rather than merely started: its job changes with the build.
	//
	// A rebuild or an upgrade can run `termit-uv init` several times in a few minutes, and every run restarts
	// the agent; without clearing the start limit the fifth run would leave it failed with "started too
	// often", which a re-runnable command must never do to its own service.
	_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reset-failed", service.AgentUnitName}})
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"enable", "--now", service.AgentUnitName}}); err != nil {
		return fmt.Errorf("enable %s: %w", service.AgentUnitName, err)
	}
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.AgentUnitName}}); err != nil {
		return fmt.Errorf("restart %s: %w", service.AgentUnitName, err)
	}
	if !s.e.serviceActive(ctx, service.AgentUnitName) {
		return fmt.Errorf("%s did not stay running", service.AgentUnitName)
	}

	// The guard exists only when the operator asked for it. Restarting it is what makes a new binary or
	// a changed ladder take effect, exactly like the agent.
	if s.e.cfg.Security.Guard.Enabled {
		_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reset-failed", service.GuardUnitName}})
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"enable", "--now", service.GuardUnitName}}); err != nil {
			return fmt.Errorf("enable %s: %w", service.GuardUnitName, err)
		}
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.GuardUnitName}}); err != nil {
			return fmt.Errorf("restart %s: %w", service.GuardUnitName, err)
		}
		if !s.e.serviceActive(ctx, service.GuardUnitName) {
			return fmt.Errorf("%s did not stay running", service.GuardUnitName)
		}
	}

	// The blocklist timer fetches the list a minute after it starts and then once per interval. It is a
	// timer, so `enable --now` only schedules it; the first fetch is the oneshot the operator can also
	// run by hand.
	if s.e.cfg.Security.Firewall.BlocklistURL != "" {
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"enable", "--now", service.BlocklistTimerName}}); err != nil {
			return fmt.Errorf("enable %s: %w", service.BlocklistTimerName, err)
		}
	}

	// The boot unit is enabled so a confirmed table comes back by itself. Enabling it does not apply
	// anything: `firewall restore` reads the record and does nothing when there is none.
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"enable", service.FirewallBootUnitName}}); err != nil {
		return fmt.Errorf("enable %s: %w", service.FirewallBootUnitName, err)
	}

	s.e.log.Info("services are up", "units", service.XrayUnitName+", "+nginxUnit+", "+service.AgentUnitName)
	return nil
}
