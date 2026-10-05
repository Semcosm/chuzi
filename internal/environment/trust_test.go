package environment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTrustStoreAcceptsBase64AndHexKeys(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trust.json")
	data, err := json.Marshal(map[string]string{
		"base64-signer": base64.StdEncoding.EncodeToString(pub),
		"hex-signer":    hex.EncodeToString(pub),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || !ed25519.PublicKey(loaded["base64-signer"]).Equal(pub) || !ed25519.PublicKey(loaded["hex-signer"]).Equal(pub) {
		t.Fatalf("loaded trust store = %#v", loaded)
	}
}

func TestLoadTrustStoreRejectsRelativeEmptyAndTrailingFiles(t *testing.T) {
	if _, err := LoadTrustStore("trust.json"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("relative trust path error = %v", err)
	}
	for name, content := range map[string]string{
		"empty.json":    "{}",
		"trailing.json": "{\"signer\":\"bad\"} {}",
	} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTrustStore(path); err == nil {
			t.Fatalf("invalid trust store %s accepted", name)
		}
	}
}

func TestLoadTrustStoreRejectsPrivateSizedKeyAndUnknownContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	for _, content := range []string{
		"{\"signer\":\"" + strings.Repeat("a", ed25519.PrivateKeySize*2) + "\"}",
		"{\"signer\":\"" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", ed25519.PrivateKeySize))) + "\"}",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTrustStore(path); err == nil {
			t.Fatal("invalid trust key accepted")
		}
	}
}
