package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSavePreservesPreviousConfigOnValidationFailure(t *testing.T) {
	cfg, _ := New(t.TempDir())
	path := filepath.Join(cfg.DataDir, "core-config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	invalid := cfg
	invalid.DataDir = "relative"
	if err := Save(path, invalid); err == nil {
		t.Fatal("invalid configuration saved")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("previous configuration changed")
	}
	cfg.Credentials.KeyEnv = "DEPLOYMENT_KEY"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("roundtrip: %#v %v", loaded, err)
	}
	temporary, _ := filepath.Glob(filepath.Join(cfg.DataDir, ".core-config-*"))
	if len(temporary) != 0 {
		t.Fatal("temporary config left behind")
	}
}
