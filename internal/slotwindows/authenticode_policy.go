package slotwindows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const authenticodePolicyFormat = "chuzi-authenticode-policy/v1"

var (
	ErrAuthenticodePolicy       = errors.New("slotwindows: invalid Authenticode policy")
	ErrAuthenticodeSigner       = errors.New("slotwindows: Authenticode signer is not trusted")
	ErrAuthenticodeTimestamp    = errors.New("slotwindows: Authenticode timestamp is missing")
	authenticodeThumbprintRegex = regexp.MustCompile("^[0-9A-Fa-f]{40}$")
)

// AuthenticodePolicy is the installation-owned publisher binding. The policy
// is shipped in the signed release manifest and is never taken from Core/UI
// input. Empty thumbprints intentionally make development packages unusable
// as production session runtimes.
type AuthenticodePolicy struct {
	Format                   string   `json:"format"`
	PublisherSubjectPrefix   string   `json:"publisher_subject_prefix"`
	AllowedThumbprints       []string `json:"allowed_thumbprints"`
	RequiredTimestamp        bool     `json:"required_timestamp"`
	Revocation               string   `json:"revocation"`
	OfflineValidation        string   `json:"offline_validation"`
	RotationOverlapDays      int      `json:"rotation_overlap_days"`
	RequiredFiles            []string `json:"required_files"`
	ProductionRequiredFiles  []string `json:"production_required_files"`
	Status                   string   `json:"status,omitempty"`
	ArtifactKind             string   `json:"artifact_kind,omitempty"`
	SigningOrder             []string `json:"signing_order,omitempty"`
	ProductionReleaseAllowed bool     `json:"production_release_allowed,omitempty"`
}

func LoadAuthenticodePolicy(path string) (AuthenticodePolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AuthenticodePolicy{}, fmt.Errorf("%w: %v", ErrAuthenticodePolicy, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy AuthenticodePolicy
	if err := decoder.Decode(&policy); err != nil {
		return AuthenticodePolicy{}, fmt.Errorf("%w: %v", ErrAuthenticodePolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AuthenticodePolicy{}, ErrAuthenticodePolicy
	}
	if err := policy.Validate(); err != nil {
		return AuthenticodePolicy{}, err
	}
	return policy, nil
}

func (p AuthenticodePolicy) Validate() error {
	if p.Format != authenticodePolicyFormat || strings.TrimSpace(p.PublisherSubjectPrefix) == "" {
		return ErrAuthenticodePolicy
	}
	if p.Revocation != "timestamp-online-cached-offline" || p.OfflineValidation != "cached-chain-and-timestamp" || !p.RequiredTimestamp || p.RotationOverlapDays < 0 || len(p.RequiredFiles) == 0 || len(p.ProductionRequiredFiles) == 0 {
		return ErrAuthenticodePolicy
	}
	if p.ArtifactKind != "" && p.ArtifactKind != "payload" && p.ArtifactKind != "ui" && p.ArtifactKind != "installer" {
		return ErrAuthenticodePolicy
	}
	seen := make(map[string]struct{}, len(p.AllowedThumbprints))
	for _, value := range p.AllowedThumbprints {
		value = strings.ToUpper(strings.ReplaceAll(value, " ", ""))
		if !authenticodeThumbprintRegex.MatchString(value) {
			return ErrAuthenticodePolicy
		}
		if _, ok := seen[value]; ok {
			return ErrAuthenticodePolicy
		}
		seen[value] = struct{}{}
	}
	return nil
}

func (p AuthenticodePolicy) Allows(thumbprint, subject string) bool {
	thumbprint = strings.ToUpper(strings.ReplaceAll(thumbprint, " ", ""))
	if !strings.HasPrefix(strings.ToLower(subject), strings.ToLower(p.PublisherSubjectPrefix)) {
		return false
	}
	for _, allowed := range p.AllowedThumbprints {
		if strings.EqualFold(strings.ReplaceAll(allowed, " ", ""), thumbprint) {
			return true
		}
	}
	return false
}
