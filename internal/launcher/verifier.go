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

// FileVerifier checks all resources declared by a release manifest. It does
// not mutate the installation; repair is a separate adapter operation.
type FileVerifier struct{}

func (FileVerifier) Verify(ctx context.Context, installRoot string, manifest ReleaseManifest) (VerificationResult, error) {
	if ctx == nil {
		return VerificationResult{}, fmt.Errorf("%w: nil context", ErrInvalidManifest)
	}
	if err := manifest.Validate(); err != nil {
		return VerificationResult{}, err
	}
	if installRoot == "" || !filepath.IsAbs(installRoot) {
		return VerificationResult{}, fmt.Errorf("%w: install root must be absolute", ErrInvalidPath)
	}
	result := VerificationResult{Valid: true}
	for _, resource := range manifest.SortedResources() {
		select {
		case <-ctx.Done():
			return VerificationResult{}, ctx.Err()
		default:
		}
		absolute, err := safeJoin(installRoot, resource.Path)
		if err != nil {
			return VerificationResult{}, err
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				result.Valid = false
				result.Issues = append(result.Issues, VerificationIssue{Path: resource.Path, Kind: "missing", Expected: resource.SHA256})
				continue
			}
			return VerificationResult{}, fmt.Errorf("stat %s: %w", resource.Path, err)
		}
		if !info.Mode().IsRegular() {
			result.Valid = false
			result.Issues = append(result.Issues, VerificationIssue{Path: resource.Path, Kind: "not_regular"})
			continue
		}
		if resource.Size != info.Size() {
			result.Valid = false
			result.Issues = append(result.Issues, VerificationIssue{Path: resource.Path, Kind: "size_mismatch", Expected: fmt.Sprint(resource.Size), Actual: fmt.Sprint(info.Size())})
			continue
		}
		actual, err := fileSHA256(ctx, absolute)
		if err != nil {
			return VerificationResult{}, fmt.Errorf("hash %s: %w", resource.Path, err)
		}
		if actual != resource.SHA256 {
			result.Valid = false
			result.Issues = append(result.Issues, VerificationIssue{Path: resource.Path, Kind: "hash_mismatch", Expected: resource.SHA256, Actual: actual})
		}
	}
	sort.Slice(result.Issues, func(i, j int) bool { return result.Issues[i].Path < result.Issues[j].Path })
	return result, nil
}

func safeJoin(root, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, relative)
	}
	return filepath.Join(root, clean), nil
}

func fileSHA256(ctx context.Context, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
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
