package service

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type BootstrapResult struct {
	Deployment journal.Deployment
	Created    bool
}

// Bootstrap performs the explicit runtime-administrator enrollment step. Serve
// never calls it and never generates or replaces controller authority.
func Bootstrap(ctx context.Context, cfg BootstrapConfig) (BootstrapResult, error) {
	if ctx == nil || cfg.Format != BootstrapFormat || !validAbsolute(cfg.JournalDir) || !validController(cfg.Controller) {
		return BootstrapResult{}, ErrConfig
	}
	public, err := controllerPublicKey(cfg)
	if err != nil {
		return BootstrapResult{}, ErrConfig
	}
	store, err := journal.Open(cfg.JournalDir)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer store.Close()
	deployment, created, err := store.EnrollController(ctx, journal.ControllerEnrollment{
		DeploymentID: cfg.Controller.DeploymentID, ControllerEpoch: cfg.Controller.ControllerEpoch,
		ControlRevision: cfg.Controller.ControlRevision, PublicKey: public, Fingerprint: cfg.Controller.Fingerprint,
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{Deployment: deployment, Created: created}, nil
}
