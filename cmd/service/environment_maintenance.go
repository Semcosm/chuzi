package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/store"
)

// runEnvironmentMaintenance is the production control-plane entrypoint for
// package install, lifecycle gates, rollback, and Store promotion. It never
// accepts a runtime executable path or a trust key on the command line.
func runEnvironmentMaintenance(ctx context.Context, options serviceOptions, operation, source, id, version string) error {
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	target := serviceTarget()
	if target == "" {
		return fmt.Errorf("service: unsupported environment target")
	}
	manager, err := newConfiguredEnvironmentManager(cfg, target)
	if err != nil {
		return err
	}
	operation = strings.TrimSpace(operation)
	if operation == "install" || operation == "upgrade" {
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("service: environment source is required")
		}
		var record environment.Record
		if operation == "install" {
			record, err = manager.Install(ctx, source)
		} else {
			record, err = manager.Upgrade(ctx, source)
		}
		if err != nil {
			return err
		}
		return writeJSON(struct {
			EnvironmentID string `json:"environment_id"`
			Version       string `json:"version"`
			Digest        string `json:"manifest_digest"`
			Signer        string `json:"signer"`
			Installed     bool   `json:"installed"`
		}{record.EnvironmentID, record.Version, record.ManifestDigest, record.Signer, record.Installed})
	}
	if operation == "promote" {
		database, err := store.Open(cfg)
		if err != nil {
			return err
		}
		defer database.Close()
		if err := manager.PromoteReady(database); err != nil {
			return err
		}
		// Mirror disabled/unhealthy records and remove stale projections so a
		// prior ready record cannot survive an explicit promotion command.
		return manager.SyncRecords(database)
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(version) == "" {
		return fmt.Errorf("service: environment id and version are required")
	}
	var record environment.Record
	switch operation {
	case "trust":
		record, err = manager.SetTrusted(id, version, true)
	case "enable":
		record, err = manager.SetEnabled(id, version, true)
	case "disable":
		record, err = manager.SetEnabled(id, version, false)
	case "health":
		record, err = manager.HealthCheck(ctx, id, version)
	case "rollback":
		err = manager.Rollback(id, version)
		if err == nil {
			record, err = manager.Get(id, version)
		}
	default:
		return fmt.Errorf("service: unknown environment operation")
	}
	if err != nil {
		return err
	}
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := manager.SyncRecords(database); err != nil {
		return err
	}
	return writeJSON(record)
}
