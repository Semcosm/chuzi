package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileSettingsStore persists launcher behavior separately from the release
// manifest and manager state. A missing file is a first-run installation and
// returns the supplied defaults; malformed existing settings fail closed.
type FileSettingsStore struct {
	Path     string
	Defaults BehaviorSettings
}

func NewFileSettingsStore(path string, defaults BehaviorSettings) (FileSettingsStore, error) {
	store := FileSettingsStore{Path: path, Defaults: defaults}
	if err := store.validate(); err != nil {
		return FileSettingsStore{}, err
	}
	return store, nil
}

func (s FileSettingsStore) validate() error {
	if s.Path == "" || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path {
		return fmt.Errorf("%w: settings path must be an absolute clean path", ErrInvalidPath)
	}
	if err := s.Defaults.Validate(); err != nil {
		return err
	}
	return nil
}

func (s FileSettingsStore) Load(ctx context.Context) (BehaviorSettings, error) {
	if err := contextErr(ctx); err != nil {
		return BehaviorSettings{}, err
	}
	if err := s.validate(); err != nil {
		return BehaviorSettings{}, err
	}
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return s.Defaults, nil
	}
	if err != nil {
		return BehaviorSettings{}, fmt.Errorf("read launcher settings: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var settings BehaviorSettings
	if err := decoder.Decode(&settings); err != nil {
		return BehaviorSettings{}, fmt.Errorf("decode launcher settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return BehaviorSettings{}, fmt.Errorf("%w: settings contain trailing JSON", ErrInvalidManifest)
		}
		return BehaviorSettings{}, fmt.Errorf("%w: settings trailing content: %v", ErrInvalidManifest, err)
	}
	if err := settings.Validate(); err != nil {
		return BehaviorSettings{}, fmt.Errorf("validate launcher settings: %w", err)
	}
	return settings, nil
}

func (s FileSettingsStore) Save(ctx context.Context, settings BehaviorSettings) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := s.validate(); err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("validate launcher settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create launcher settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".launcher-settings-")
	if err != nil {
		return fmt.Errorf("stage launcher settings: %w", err)
	}
	temporaryName := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict launcher settings: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(settings); err != nil {
		return fmt.Errorf("encode launcher settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync launcher settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close launcher settings: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("commit launcher settings: %w", err)
	}
	ok = true
	return nil
}
