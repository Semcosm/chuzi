package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// FileRepairer restores resources from a trusted local source tree. Every
// source is checked against the manifest before any destination is changed.
// Destination replacement is committed as one filesystem transaction and is
// rolled back if any rename fails.
type FileRepairer struct{ SourceRoot string }

type LocalResourceRepairer = FileRepairer

func (r FileRepairer) Repair(ctx context.Context, request RepairRequest) (RepairResult, error) {
	if err := contextErr(ctx); err != nil {
		return RepairResult{}, err
	}
	if err := request.Manifest.Validate(); err != nil {
		return RepairResult{}, err
	}
	if request.InstallRoot == "" || !filepath.IsAbs(request.InstallRoot) {
		return RepairResult{}, fmt.Errorf("%w: install root must be absolute", ErrInvalidPath)
	}
	if r.SourceRoot == "" || !filepath.IsAbs(r.SourceRoot) {
		return RepairResult{}, fmt.Errorf("%w: source root must be absolute", ErrInvalidPath)
	}
	resources := make(map[string]Resource)
	for _, resource := range request.Manifest.SortedResources() {
		resources[resource.Path] = resource
	}
	selected := request.Paths
	if len(selected) == 0 {
		selected = make([]string, 0, len(resources))
		for resourcePath := range resources {
			selected = append(selected, resourcePath)
		}
		sort.Strings(selected)
	}
	seen := make(map[string]struct{}, len(selected))
	type pending struct {
		resource Resource
		target   string
		staged   string
	}
	var repairs []pending
	result := RepairResult{}
	for _, resourcePath := range selected {
		if _, ok := seen[resourcePath]; ok {
			continue
		}
		seen[resourcePath] = struct{}{}
		resource, ok := resources[resourcePath]
		if !ok {
			return RepairResult{}, fmt.Errorf("%w: resource %q is not in manifest", ErrInvalidManifest, resourcePath)
		}
		target, err := safeJoin(request.InstallRoot, resource.Path)
		if err != nil {
			return RepairResult{}, err
		}
		valid, err := verifyOne(ctx, target, resource)
		if err != nil {
			return RepairResult{}, err
		}
		if valid {
			result.Skipped = append(result.Skipped, resource.Path)
			continue
		}
		source, err := safeJoin(r.SourceRoot, resource.Path)
		if err != nil {
			return RepairResult{}, err
		}
		if ok, err := verifyOne(ctx, source, resource); err != nil {
			return RepairResult{}, fmt.Errorf("verify repair source %s: %w", resource.Path, err)
		} else if !ok {
			return RepairResult{}, fmt.Errorf("%w: repair source %q does not match manifest", ErrInvalidManifest, resource.Path)
		}
		repairs = append(repairs, pending{resource: resource, target: target})
	}
	if len(repairs) == 0 {
		sort.Strings(result.Skipped)
		return result, nil
	}
	if err := os.MkdirAll(request.InstallRoot, 0o755); err != nil {
		return RepairResult{}, fmt.Errorf("create install root: %w", err)
	}
	stageDir, err := os.MkdirTemp(request.InstallRoot, ".chuzi-repair-")
	if err != nil {
		return RepairResult{}, fmt.Errorf("create repair stage: %w", err)
	}
	defer os.RemoveAll(stageDir)
	for index := range repairs {
		if err := contextErr(ctx); err != nil {
			return RepairResult{}, err
		}
		staged := filepath.Join(stageDir, fmt.Sprintf("%06d", index))
		if err := copyVerified(ctx, safeSourcePath(r.SourceRoot, repairs[index].resource.Path), staged, repairs[index].resource); err != nil {
			return RepairResult{}, fmt.Errorf("stage %s: %w", repairs[index].resource.Path, err)
		}
		repairs[index].staged = staged
	}
	type backup struct {
		target, backup string
		hadOriginal    bool
		installed      bool
	}
	backups := make([]backup, 0, len(repairs))
	rollback := func() {
		for index := len(backups) - 1; index >= 0; index-- {
			item := backups[index]
			if item.installed {
				_ = os.Remove(item.target)
			}
			if item.hadOriginal {
				_ = os.Rename(item.backup, item.target)
			}
		}
	}
	for _, item := range repairs {
		if err := contextErr(ctx); err != nil {
			rollback()
			return RepairResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(item.target), 0o755); err != nil {
			rollback()
			return RepairResult{}, fmt.Errorf("create resource directory: %w", err)
		}
		backupPath := filepath.Join(stageDir, fmt.Sprintf("backup-%d", len(backups)))
		entry := backup{target: item.target, backup: backupPath}
		if _, err := os.Lstat(item.target); err == nil {
			if err := os.Rename(item.target, backupPath); err != nil {
				rollback()
				return RepairResult{}, fmt.Errorf("backup %s: %w", item.resource.Path, err)
			}
			entry.hadOriginal = true
		} else if !os.IsNotExist(err) {
			rollback()
			return RepairResult{}, fmt.Errorf("inspect %s: %w", item.resource.Path, err)
		}
		backups = append(backups, entry)
		if err := os.Rename(item.staged, item.target); err != nil {
			rollback()
			return RepairResult{}, fmt.Errorf("install %s: %w", item.resource.Path, err)
		}
		backups[len(backups)-1].installed = true
		result.Repaired = append(result.Repaired, item.resource.Path)
	}
	sort.Strings(result.Repaired)
	sort.Strings(result.Skipped)
	return result, nil
}

func safeSourcePath(root, resourcePath string) string {
	path, _ := safeJoin(root, resourcePath)
	return path
}

func verifyOne(ctx context.Context, filename string, resource Resource) (bool, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != resource.Size {
		return false, nil
	}
	actual, err := hashFile(ctx, filename)
	if err != nil {
		return false, err
	}
	return actual == resource.SHA256, nil
}

func copyVerified(ctx context.Context, source, target string, resource Resource) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(output, hash), input)
	if err != nil {
		return err
	}
	if count != resource.Size || hex.EncodeToString(hash.Sum(nil)) != resource.SHA256 {
		return fmt.Errorf("%w: staged resource does not match manifest", ErrInvalidManifest)
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func hashFile(ctx context.Context, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := contextErr(ctx); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if readErr == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}
