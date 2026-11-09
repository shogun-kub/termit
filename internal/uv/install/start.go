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
	return CheckResult{Satisfied: true, Detail: "the core, nginx and the renewal timer are running"}, nil
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

	// The API answers only when the core parsed the config and started its handler.
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
	s.e.log.Info("services are up", "units", service.XrayUnitName+", "+nginxUnit)
	return nil
}
