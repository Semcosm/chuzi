//go:build !windows

package main

import (
	"os/exec"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
)

func resolveServiceWorkerCommand(options serviceOptions, _ bool) (string, error) {
	return exec.LookPath(options.workerCommand)
}

func configureWorkerFactory(_ config.Config, _ serviceOptions, factory browser.WorkerFactory, _ slotProfileAccess) (browser.WorkerFactory, error) {
	return factory, nil
}

func newSlotReconciler(config config.Config, options serviceOptions, database *store.Store, now func() time.Time, _ slotCapabilityRevoker, _ *serviceEnvironmentRuntime, _ slot.PoolConfig) (slotReconciler, slotProfileAccess, error) {
	if config.WindowsJobPool.Enabled {
		return nil, nil, slotwindows.ErrUnsupported
	}
	reconciler, err := newLogicalSlotReconciler(database, now, options.owner)
	if err != nil {
		return nil, nil, err
	}
	return reconciler, nil, nil
}
