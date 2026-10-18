package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/store"
)

func TestServicePoolModeAPIWritesThroughOpenStore(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cfg.DataDir, "core-config.json")
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	api := &servicePoolModeAPI{configPath: configPath, cfg: cfg, database: database}
	result, err := api.ConfigurePoolMode(context.Background(), coreapi.PoolModeRequest{Mode: "logical"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfiguredPoolMode != "logical" || result.Status != "restart_required" {
		t.Fatalf("result = %#v", result)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WindowsJobPool.Enabled {
		t.Fatalf("logical mode left Windows config enabled: %#v", loaded.WindowsJobPool)
	}
	if _, err := database.OperationalSnapshot(time.Now().UTC()); err != nil {
		t.Fatalf("store became unusable after config save: %v", err)
	}
}

func TestServicePoolModeAPIRejectsInvalidModeWithoutWriting(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cfg.DataDir, "core-config.json")
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	api := &servicePoolModeAPI{configPath: configPath, cfg: cfg, database: database}
	if _, err := api.ConfigurePoolMode(context.Background(), coreapi.PoolModeRequest{Mode: "unsupported"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.WindowsJobPool.Enabled {
		t.Fatal("invalid mode changed config")
	}
}
