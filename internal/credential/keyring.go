package credential

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
)

// StaticKeyring is an in-memory keyring intended for tests and embedded
// adapters. Production callers should use a deployment secret manager.
type StaticKeyring struct {
	mu      sync.RWMutex
	keys    map[string]Key
	current string
}

// NewStaticKeyring creates a keyring with current and optional historical
// keys. All keys are copied.
func NewStaticKeyring(current Key, historical ...Key) (*StaticKeyring, error) {
	if err := current.validate(); err != nil {
		return nil, ErrInvalidKey
	}
	result := &StaticKeyring{keys: make(map[string]Key), current: current.ID()}
	result.keys[current.ID()] = cloneKey(current)
	for _, key := range historical {
		if err := key.validate(); err != nil {
			return nil, ErrInvalidKey
		}
		result.keys[key.ID()] = cloneKey(key)
	}
	return result, nil
}

// Add registers a historical or current key without changing the current
// selection.
func (k *StaticKeyring) Add(key Key) error {
	if k == nil {
		return ErrInvalidKey
	}
	if err := key.validate(); err != nil {
		return ErrInvalidKey
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = make(map[string]Key)
	}
	k.keys[key.ID()] = cloneKey(key)
	return nil
}

// SetCurrent registers a key and makes it the current encryption key.
func (k *StaticKeyring) SetCurrent(key Key) error {
	if err := k.Add(key); err != nil {
		return err
	}
	k.mu.Lock()
	k.current = key.ID()
	k.mu.Unlock()
	return nil
}

func (k *StaticKeyring) Current(ctx context.Context) (Key, error) {
	if err := checkContext(ctx); err != nil {
		return Key{}, err
	}
	if k == nil {
		return Key{}, ErrKeyUnavailable
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	key, ok := k.keys[k.current]
	if !ok {
		return Key{}, ErrKeyUnavailable
	}
	return cloneKey(key), nil
}

func (k *StaticKeyring) Lookup(ctx context.Context, id string) (Key, error) {
	if err := checkContext(ctx); err != nil {
		return Key{}, err
	}
	if k == nil || strings.TrimSpace(id) == "" {
		return Key{}, ErrKeyUnavailable
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	key, ok := k.keys[id]
	if !ok {
		return Key{}, ErrKeyUnavailable
	}
	return cloneKey(key), nil
}

func cloneKey(key Key) Key {
	return Key{id: key.id, material: append([]byte(nil), key.material...)}
}

// EnvKeyring reads one current key from deployment environment variables. A
// deployment that performs rotation must provide a Keyring retaining previous
// keys; this adapter intentionally refuses to guess historical material.
type EnvKeyring struct {
	KeyEnv string
	IDEnv  string
}

// NewEnvKeyring uses CHUZI_CREDENTIAL_KEY and CHUZI_CREDENTIAL_KEY_ID when
// names are empty. The key value is base64 (standard or raw) or hexadecimal.
func NewEnvKeyring(keyEnv, idEnv string) EnvKeyring {
	if strings.TrimSpace(keyEnv) == "" {
		keyEnv = "CHUZI_CREDENTIAL_KEY"
	}
	if strings.TrimSpace(idEnv) == "" {
		idEnv = "CHUZI_CREDENTIAL_KEY_ID"
	}
	return EnvKeyring{KeyEnv: keyEnv, IDEnv: idEnv}
}

func (e EnvKeyring) Current(ctx context.Context) (Key, error) {
	if err := checkContext(ctx); err != nil {
		return Key{}, err
	}
	keyName, idName := e.names()
	id := strings.TrimSpace(os.Getenv(idName))
	encoded := strings.TrimSpace(os.Getenv(keyName))
	if id == "" || encoded == "" {
		return Key{}, ErrKeyUnavailable
	}
	material, err := decodeMaterial(encoded)
	if err != nil {
		return Key{}, ErrKeyUnavailable
	}
	key, err := NewKey(id, material)
	clear(material)
	if err != nil {
		return Key{}, ErrKeyUnavailable
	}
	return key, nil
}

func (e EnvKeyring) Lookup(ctx context.Context, id string) (Key, error) {
	current, err := e.Current(ctx)
	if err != nil || current.ID() != id {
		return Key{}, ErrKeyUnavailable
	}
	return current, nil
}

func (e EnvKeyring) names() (string, string) {
	keyName := strings.TrimSpace(e.KeyEnv)
	idName := strings.TrimSpace(e.IDEnv)
	if keyName == "" {
		keyName = "CHUZI_CREDENTIAL_KEY"
	}
	if idName == "" {
		idName = "CHUZI_CREDENTIAL_KEY_ID"
	}
	return keyName, idName
}

func decodeMaterial(encoded string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		material, err := decode(encoded)
		if err == nil && (len(material) == 16 || len(material) == 24 || len(material) == 32) {
			return material, nil
		}
	}
	return nil, errors.New("invalid key encoding")
}
