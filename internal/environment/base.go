package environment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const BaseSlotEnvironmentID = "chuzi-slot-base"
const BaseSlotEnvironmentVersion = "1.0.0"

// BaseSlotManifest returns the unsigned, versioned metadata template used by
// signing/build tooling. The signer and signature are intentionally supplied
// by deployment packaging; no private key belongs in this repository.
func BaseSlotManifest() Manifest {
	workerContract := BaseSlotContractBytes()
	digest := sha256.Sum256(workerContract)
	return Manifest{
		API: API, EnvironmentID: BaseSlotEnvironmentID, Version: BaseSlotEnvironmentVersion,
		Targets:         []string{"windows-amd64"},
		Capabilities:    []string{"windows-desktop", "browser-worker", "slot-agent"},
		Permissions:     []string{"controlled-desktop", "named-pipe-health", "job-object"},
		Resources:       []Resource{{Path: "base-runtime-contract.json", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(workerContract))}},
		Dependencies:    []string{},
		InstallPolicy:   InstallPolicy{Atomic: true},
		CleanupPolicy:   CleanupPolicy{RemoveResources: true, RetainRollback: true},
		HealthProbes:    []HealthProbe{{Name: "agent", Kind: "protocol", Target: "slot-agent", TimeoutSeconds: 10}, {Name: "desktop", Kind: "desktop", Target: "controlled-desktop", TimeoutSeconds: 10}},
		Entrypoints:     []Entrypoint{{Name: "worker", Path: "base-runtime-contract.json", Runtime: "browser-worker"}, {Name: "headless", Path: "base-runtime-contract.json", Runtime: "browser-worker"}},
		RuntimeContract: &RuntimeContract{Kind: "base-slot", ServiceOwned: []string{"node-runtime", "chuzi-user-agent", "session-supervisor", "adapter-bridge"}, Entrypoints: []string{"worker", "headless"}, Isolation: []string{"managed-user", "profile", "desktop", "job-object", "named-pipe", "health-protocol"}},
		Signer:          "chuzi-release",
	}
}

// BaseSlotContractBytes is the deterministic resource payload used by the
// fixture/package builder. It contains no executable, shell or adapter data.
func BaseSlotContractBytes() []byte {
	value, _ := json.Marshal(map[string]string{"kind": "base-slot", "protocol": "chuzi.slot-runtime/v1"})
	return value
}
