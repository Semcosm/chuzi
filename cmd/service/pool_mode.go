package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

// This maintenance path never provisions or removes OS resources. Both
// directions require completed cleanup under the previous runtime first.
type poolModeError string

func (e poolModeError) Error() string { return string(e) }

func runPoolModeMaintenance(ctx context.Context, configPath, mode, poolID string, revision uint64) error {
	if mode != "logical" && mode != "windows" {
		return poolModeError("invalid_pool_mode")
	}
	if mode == "windows" && runtime.GOOS != "windows" {
		return poolModeError("windows_required")
	}
	cfg, err := config.Load(configPath)
	path, pathErr := filepath.Abs(configPath)
	if err != nil || pathErr != nil || filepath.Dir(path) != cfg.DataDir {
		return poolModeError("core_config_invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	database, err := store.Open(cfg)
	if err != nil {
		return poolModeError("core_stop_required")
	}
	defer database.Close()
	pool, err := poolModeCandidate(cfg, database, mode, poolID, revision, time.Now().UTC())
	if err != nil {
		return err
	}
	if mode == "windows" {
		manager, err := newConfiguredEnvironmentManager(pool, serviceTarget())
		if err != nil {
			return poolModeError("environment_unavailable")
		}
		selected, err := database.GetJobPool(poolID)
		if err != nil {
			return poolModeError("pool_not_found")
		}
		if err := verifyWindowsModeEnvironment(database, manager, pool, selected); err != nil {
			return poolModeError("environment_unavailable")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.Save(configPath, pool); err != nil {
		return poolModeError("pool_mode_save_failed")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"configured_pool_mode": mode, "status": "restart_required"})
}

func verifyWindowsModeEnvironment(database *store.Store, manager *environment.Manager, cfg config.Config, pool slot.PoolConfig) error {
	record, err := database.GetEnvironmentRecord(pool.EnvironmentID, pool.EnvironmentVersion)
	if err != nil || !record.IsReady() || record.ManifestDigest != pool.ManifestDigest || record.Signer != pool.Signer {
		return poolModeError("environment_unavailable")
	}
	_, err = resolveServiceEnvironment(cfg, manager, pool, "headless")
	return err
}

func poolModeCandidate(cfg config.Config, database *store.Store, mode, poolID string, revision uint64, at time.Time) (config.Config, error) {
	pools, err := database.ListJobPools()
	if err != nil {
		return config.Config{}, err
	}
	for _, pool := range pools {
		projection, err := database.GetJobPoolProjection(pool.PoolID, at)
		if err != nil {
			return config.Config{}, err
		}
		if pool.DesiredSlots != 0 || (projection.OperationID != "" && projection.ReconcileState != store.JobPoolApplied && projection.ReconcileState != store.JobPoolFailed && projection.ReconcileState != store.JobPoolRolledBack && projection.ReconcileState != store.JobPoolCancelled) {
			return config.Config{}, poolModeError("pool_cleanup_required")
		}
	}
	leases, err := database.ListSlotLeases()
	if err != nil {
		return config.Config{}, err
	}
	snapshot, err := database.OperationalSnapshot(at)
	if err != nil {
		return config.Config{}, err
	}
	if len(leases) != 0 || snapshot.ActiveLeases+snapshot.ExpiredLeases != 0 || snapshot.ReadySlots+snapshot.LeasedSlots+snapshot.QuarantinedSlots+snapshot.DrainingSlots+snapshot.ProvisioningSlots+snapshot.RetiringSlots+snapshot.UnprovisionedSlots != 0 {
		return config.Config{}, poolModeError("pool_cleanup_required")
	}
	if mode == "logical" {
		cfg.WindowsJobPool = config.WindowsJobPoolConfig{}
		return cfg, cfg.Validate()
	}
	if mode != "windows" {
		return config.Config{}, poolModeError("invalid_pool_mode")
	}
	pool, err := database.GetJobPool(poolID)
	if errors.Is(err, slot.ErrPoolNotFound) {
		return config.Config{}, poolModeError("pool_not_found")
	}
	if err != nil {
		return config.Config{}, err
	}
	if revision == 0 || pool.ConfigRevision != revision {
		return config.Config{}, poolModeError("stale_revision")
	}
	if len(pool.ManifestDigest) != 64 || pool.Signer == "" || !pool.RequireTrusted {
		return config.Config{}, poolModeError("signed_environment_required")
	}
	cfg.JobPool = config.JobPoolConfig{PoolID: pool.PoolID, EnvironmentID: pool.EnvironmentID, EnvironmentVersion: pool.EnvironmentVersion, DesiredSlots: 0, Capabilities: pool.Capabilities, ManifestDigest: pool.ManifestDigest, Signer: pool.Signer, RequireTrusted: true}
	cfg.EnvironmentPackage = config.EnvironmentPackageConfig{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, ManifestDigest: pool.ManifestDigest, Signer: pool.Signer}
	if !cfg.WindowsJobPool.Enabled {
		cfg.WindowsJobPool = config.WindowsJobPoolConfig{Enabled: true, UserPrefix: "ChuziJob", AgentHeartbeatSeconds: 5, ProvisionTimeoutSeconds: 120, CleanupTimeoutSeconds: 120}
	}
	cfg.WindowsJobPool.DesiredSlots = 0
	cfg.WindowsJobPool.EnvironmentID = pool.EnvironmentID
	cfg.WindowsJobPool.EnvironmentVersion = pool.EnvironmentVersion
	return cfg, cfg.Validate()
}
