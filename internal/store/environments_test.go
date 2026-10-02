package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

func TestEnvironmentRecordLifecycleProjectionIsDurable(t *testing.T) {
	database, _ := openTestStore(t)
	record := environment.Record{
		EnvironmentID:  "chuzi-environment/v1",
		Version:        "1.0.0",
		Capabilities:   []string{"cdp", "windows-desktop"},
		ManifestDigest: strings.Repeat("a", 64),
		Signer:         "test-signer",
		Installed:      true,
		Verified:       true,
		Trusted:        true,
		Enabled:        true,
		Healthy:        true,
		Ready:          true,
		Generation:     1,
		UpdatedAt:      storeTestTime,
	}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetEnvironmentRecord(record.EnvironmentID, record.Version)
	if err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("environment record = %#v, %v", got, err)
	}
	list, err := database.ListEnvironmentRecords()
	if err != nil || len(list) != 1 || !list[0].IsReady() {
		t.Fatalf("environment records = %#v, %v", list, err)
	}
	if err := database.ValidateDatabase(); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedPoolReconcileRequiresReadyEnvironmentRecord(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("b", 64)
	pool := slot.PoolConfig{PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "signer", RequireTrusted: true}
	if err := database.ReconcileTrustedJobPool(pool, storeTestTime); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("missing environment record error = %v", err)
	}
	record := environment.Record{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTrustedJobPool(pool, storeTestTime.Add(time.Second)); err != nil {
		t.Fatalf("ready environment reconcile = %v", err)
	}
	if _, err := database.GetSlot("pool-001"); err != nil {
		t.Fatal(err)
	}
}

func TestSlotClaimStopsWhenDurableEnvironmentLosesReadiness(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("c", 64)
	pool := slot.PoolConfig{PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "signer", RequireTrusted: true}
	record := environment.Record{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: append([]string(nil), pool.Capabilities...), ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTrustedJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	item, err := database.GetSlot("pool-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(item.SlotID, slot.EnvironmentSummary{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Generation: 1, Capabilities: pool.Capabilities, ManifestDigest: digest, Signer: pool.Signer, Trusted: true, AgentVersion: "agent", SessionState: "ready", DesktopReady: true, UpdatedAt: storeTestTime}, storeTestTime); err != nil {
		t.Fatal(err)
	}
	record.Trusted, record.Enabled, record.Healthy, record.Ready = false, false, false, false
	record.UpdatedAt = storeTestTime.Add(time.Second)
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.AcquireSlotLease(storeTestTime.Add(2*time.Second), pool.PoolID, pool.Requirement(), "request-1", "account-1", "owner-1", "lease-1", time.Minute); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("claim after trust loss = %v", err)
	}
}

func TestValidateDatabaseRejectsPoolWhenEnvironmentRecordLosesReadiness(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("d", 64)
	pool := slot.PoolConfig{PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 0, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "signer", RequireTrusted: true}
	record := environment.Record{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: append([]string(nil), pool.Capabilities...), ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTrustedJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	record.Trusted, record.Enabled, record.Healthy, record.Ready = false, false, false, false
	record.UpdatedAt = storeTestTime.Add(time.Second)
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := database.ValidateDatabase(); !errors.Is(err, ErrCorruptData) {
		t.Fatalf("pool with non-ready environment validation = %v", err)
	}
}
