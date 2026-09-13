package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ManifestSource supplies a candidate release manifest. Network adapters can
// implement this boundary without making the launcher depend on a protocol.
type ManifestSource interface {
	Fetch(context.Context, UpdateRequest) (ReleaseManifest, error)
}

// StaticManifestSource is useful for embedding a signed manifest or tests.
type StaticManifestSource struct{ Manifest ReleaseManifest }

func (s StaticManifestSource) Fetch(ctx context.Context, _ UpdateRequest) (ReleaseManifest, error) {
	select {
	case <-ctx.Done():
		return ReleaseManifest{}, ctx.Err()
	default:
	}
	return s.Manifest, nil
}

// FileManifestSource reads a manifest supplied by an operator or a download
// adapter. It deliberately does not download or execute anything.
type FileManifestSource struct{ Path string }

func (s FileManifestSource) Fetch(ctx context.Context, _ UpdateRequest) (ReleaseManifest, error) {
	if err := contextErr(ctx); err != nil {
		return ReleaseManifest{}, err
	}
	if strings.TrimSpace(s.Path) == "" {
		return ReleaseManifest{}, fmt.Errorf("%w: manifest source path is required", ErrInvalidPath)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return ReleaseManifest{}, fmt.Errorf("read update manifest: %w", err)
	}
	var manifest ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ReleaseManifest{}, fmt.Errorf("decode update manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ReleaseManifest{}, fmt.Errorf("%w: update manifest has trailing JSON", ErrInvalidManifest)
		}
		return ReleaseManifest{}, fmt.Errorf("%w: update manifest trailing content: %v", ErrInvalidManifest, err)
	}
	if err := manifest.Validate(); err != nil {
		return ReleaseManifest{}, err
	}
	return manifest, nil
}

// ManifestUpdateChecker validates source metadata before reporting an update.
// Endpoint reachability or a downloaded file alone never means an update is
// available; the candidate must match the requested target and channel.
type ManifestUpdateChecker struct{ Source ManifestSource }

// FileUpdateChecker is a convenience adapter for a candidate manifest on
// disk. It keeps the UpdateChecker interface usable without a network client.
type FileUpdateChecker struct{ Path string }

func (c FileUpdateChecker) Check(ctx context.Context, request UpdateRequest) (UpdateInfo, error) {
	return (ManifestUpdateChecker{Source: FileManifestSource{Path: c.Path}}).Check(ctx, request)
}

func (c ManifestUpdateChecker) Check(ctx context.Context, request UpdateRequest) (UpdateInfo, error) {
	if err := contextErr(ctx); err != nil {
		return UpdateInfo{}, err
	}
	if strings.TrimSpace(request.CurrentVersion) == "" || strings.TrimSpace(request.Target) == "" {
		return UpdateInfo{}, fmt.Errorf("%w: current version and target are required", ErrInvalidManifest)
	}
	if request.Channel != ChannelNightly && request.Channel != ChannelStable {
		return UpdateInfo{}, fmt.Errorf("%w: update channel %q", ErrInvalidManifest, request.Channel)
	}
	if c.Source == nil {
		return UpdateInfo{}, fmt.Errorf("%w: update source is required", ErrUnsupported)
	}
	manifest, err := c.Source.Fetch(ctx, request)
	if err != nil {
		return UpdateInfo{}, err
	}
	if err := manifest.Validate(); err != nil {
		return UpdateInfo{}, err
	}
	if manifest.Target != request.Target || manifest.Channel != request.Channel {
		return UpdateInfo{}, fmt.Errorf("%w: update target/channel mismatch", ErrInvalidManifest)
	}
	if !versionGreater(manifest.Version, request.CurrentVersion) {
		return UpdateInfo{Reason: "up_to_date"}, nil
	}
	return UpdateInfo{Available: true, Manifest: &manifest, Reason: "update_available"}, nil
}

func versionGreater(candidate, current string) bool {
	candidateParts := versionNumbers(candidate)
	currentParts := versionNumbers(current)
	if len(candidateParts) == 0 || len(currentParts) == 0 {
		return candidate > current
	}
	for index := 0; index < len(candidateParts) && index < len(currentParts); index++ {
		if candidateParts[index] != currentParts[index] {
			return candidateParts[index] > currentParts[index]
		}
	}
	return len(candidateParts) > len(currentParts)
}

func versionNumbers(value string) []int {
	var numbers []int
	for index := 0; index < len(value); {
		if value[index] < '0' || value[index] > '9' {
			index++
			continue
		}
		number := 0
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			number = number*10 + int(value[index]-'0')
			index++
		}
		numbers = append(numbers, number)
	}
	return numbers
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrUnsupported)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
