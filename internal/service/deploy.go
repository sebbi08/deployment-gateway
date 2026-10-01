package service

import (
	"context"
	"time"
)

func (s *Service) Validate(ctx context.Context, p Principal, in DeployInput) (res ValidateResult, err error) {
	defer s.track(ctx, p, "validate", in, &err, time.Now())
	pl, err := s.plan(ctx, p.Workspace, in.App, &in.Stack)
	if err != nil {
		return ValidateResult{}, err
	}
	if err := s.checkPortConflicts(ctx, p.Workspace, in.App, pl.claims); err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{App: in.App, Services: pl.servicePlans(p.Workspace.Name), TotalMemoryMB: pl.memBytes >> 20, TotalCPUs: pl.cpus}, nil
}
