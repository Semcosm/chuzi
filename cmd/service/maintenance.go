package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/matrix"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/store"
)

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// cliErrorMessage is the process boundary for maintenance/startup failures.
// Internal errors can contain paths or identifiers, so the CLI reports only a
// stable category and keeps detailed values out of journals and CI logs.
func cliErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, errInvalidBackend):
		return "invalid browser backend"
	case errors.Is(err, errInvalidOptions):
		return "invalid service options"
	case errors.Is(err, config.ErrInvalidConfig):
		return "invalid configuration"
	case errors.Is(err, store.ErrInvalidRestore):
		return "invalid restore source"
	case errors.Is(err, store.ErrCorruptData):
		return "corrupt database"
	case errors.Is(err, store.ErrAccountNotFound), errors.Is(err, store.ErrRequestNotFound):
		return "requested record not found"
	case errors.Is(err, credential.ErrKeyUnavailable):
		return "credential key unavailable"
	case errors.Is(err, matrix.ErrUnauthorized):
		return "Matrix authorization failed"
	default:
		return "operation failed"
	}
}

func runDiagnostics(ctx context.Context, options serviceOptions, audit bool, auditAccount string, auditLimit int) error {
	if ctx == nil {
		return errInvalidOptions
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.ValidateDatabase(); err != nil {
		return err
	}
	if audit {
		entries, err := database.ListAuditEntries(store.AuditQuery{AccountID: strings.TrimSpace(auditAccount), Limit: auditLimit})
		if err != nil {
			return err
		}
		return writeJSON(entries)
	}
	snapshot, issues, err := database.Diagnostics(time.Now().UTC())
	if err != nil {
		return err
	}
	return writeJSON(struct {
		Snapshot store.OperationalSnapshot `json:"snapshot"`
		Issues   []store.OperationalIssue  `json:"issues"`
	}{Snapshot: snapshot, Issues: issues})
}

func runMaintenance(ctx context.Context, options serviceOptions, backup bool, restorePath string, injectAccount string, rotateAccount string, revokeAccount string, credentialEnv string, credentialActor string, diagnostics bool, audit bool, auditAccount string, validateBackupPath string, auditLimit int) error {
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	operations := 0
	if backup {
		operations++
	}
	if strings.TrimSpace(restorePath) != "" {
		operations++
	}
	if strings.TrimSpace(injectAccount) != "" {
		operations++
	}
	if strings.TrimSpace(rotateAccount) != "" {
		operations++
	}
	if strings.TrimSpace(revokeAccount) != "" {
		operations++
	}
	if diagnostics {
		operations++
	}
	if audit {
		operations++
	}
	if strings.TrimSpace(validateBackupPath) != "" {
		operations++
	}
	if operations != 1 {
		return fmt.Errorf("service: exactly one maintenance operation is required")
	}
	if backup {
		database, err := store.Open(cfg)
		if err != nil {
			return err
		}
		path, backupErr := database.Backup(time.Now().UTC())
		closeErr := database.Close()
		if backupErr != nil {
			return backupErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Println(path)
		return nil
	}
	if strings.TrimSpace(restorePath) != "" {
		if err := store.Restore(cfg, restorePath); err != nil {
			return err
		}
		fmt.Println("restore complete")
		return nil
	}
	if strings.TrimSpace(validateBackupPath) != "" {
		if err := store.ValidateBackup(validateBackupPath); err != nil {
			return err
		}
		fmt.Println("backup valid")
		return nil
	}
	if diagnostics || audit {
		return runDiagnostics(ctx, options, audit, auditAccount, auditLimit)
	}
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	keyring := credential.NewEnvKeyringWithHistory(cfg.Credentials.KeyEnv, cfg.Credentials.KeyIDEnv, cfg.Credentials.HistoryEnv)
	credentials, err := credential.New(database, keyring)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentialActor) == "" {
		credentialActor = options.owner
	}
	if strings.TrimSpace(injectAccount) != "" {
		if strings.TrimSpace(credentialEnv) == "" {
			return fmt.Errorf("service: -credential-env is required for injection")
		}
		source, sourceErr := credential.NewEnvSource(credentialEnv)
		if sourceErr != nil {
			return sourceErr
		}
		metadata, injectErr := credentials.Inject(ctx, injectAccount, credentialActor, time.Now().UTC(), source)
		if injectErr != nil {
			return injectErr
		}
		fmt.Printf("credential account=%s version=%d key_id=%s\n", observability.RedactIdentifier(metadata.AccountID), metadata.Version, observability.RedactIdentifier(metadata.KeyID))
		return nil
	}
	if strings.TrimSpace(rotateAccount) != "" {
		metadata, rotateErr := credentials.Rotate(ctx, rotateAccount, credentialActor, time.Now().UTC())
		if rotateErr != nil {
			return rotateErr
		}
		fmt.Printf("credential rotated account=%s version=%d key_id=%s\n", observability.RedactIdentifier(metadata.AccountID), metadata.Version, observability.RedactIdentifier(metadata.KeyID))
		return nil
	}
	if strings.TrimSpace(revokeAccount) != "" {
		if revokeErr := credentials.Revoke(ctx, revokeAccount, credentialActor, time.Now().UTC()); revokeErr != nil {
			return revokeErr
		}
		fmt.Printf("credential revoked account=%s\n", observability.RedactIdentifier(revokeAccount))
		return nil
	}
	return errInvalidOptions
}
