package environment

import (
	"bytes"
	"testing"
)

func TestBaseSlotManifestIsDeterministicAndUnsigned(t *testing.T) {
	first := BaseSlotManifest()
	second := BaseSlotManifest()
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if first.Signature != "" || first.SignatureAlgorithm != "" || first.ManifestDigest != "" {
		t.Fatalf("base manifest unexpectedly sealed: %#v", first)
	}
	if first.RuntimeContract == nil || first.RuntimeContract.Kind != "base-slot" {
		t.Fatalf("runtime contract = %#v", first.RuntimeContract)
	}
	if !bytes.Equal(BaseSlotContractBytes(), BaseSlotContractBytes()) || first.Resources[0].Size <= 0 || first.Resources[0].SHA256 == "" {
		t.Fatalf("invalid deterministic contract metadata: %#v", first.Resources)
	}
	if first.EnvironmentID != second.EnvironmentID || first.Version != second.Version || first.Resources[0] != second.Resources[0] {
		t.Fatalf("base manifest changed between calls: %#v vs %#v", first, second)
	}
}
