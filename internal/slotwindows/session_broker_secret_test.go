package slotwindows

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestSessionBrokerSecretRoundTripStaysBinaryAndWipesCopies(t *testing.T) {
	secret := SessionBrokerSecret{PoolID: "pool-a", SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-100", Owner: "service-owner", Username: "chuzi-slot-001", Password: []uint16{'s', 'e', 'c', 'r', 'e', 't'}}
	frame, err := encodeSessionBrokerSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(frame, []byte("\"password\"")) {
		t.Fatal("private handoff unexpectedly uses JSON field names")
	}
	decoded, err := decodeSessionBrokerSecret(frame)
	clear(frame)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SlotID != secret.SlotID || decoded.Ordinal != secret.Ordinal || decoded.Generation != secret.Generation || decoded.SID != secret.SID || decoded.Owner != secret.Owner || decoded.Username != secret.Username || !slices.Equal(decoded.Password, secret.Password) {
		t.Fatalf("decoded secret does not match ownership: %#v", decoded)
	}
	clear(decoded.Password)
}

func TestSessionBrokerSecretStoreFencesOwnershipAndConsumesOnce(t *testing.T) {
	store := NewSessionBrokerSecretStore()
	secret := SessionBrokerSecret{PoolID: "pool-a", SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-100", Owner: "service-owner", Username: "chuzi-slot-001", Password: []uint16{'s', 'e', 'c', 'r', 'e', 't'}}
	if err := store.Put(secret); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(secret); !errors.Is(err, ErrSessionBrokerSecretBusy) {
		t.Fatalf("duplicate Put error = %v", err)
	}
	wrong := secret.ownership()
	wrong.Generation++
	if _, err := store.Take(wrong); !errors.Is(err, ErrSessionBrokerSecretInvalid) {
		t.Fatalf("wrong generation Take error = %v", err)
	}
	got, err := store.Take(secret.ownership())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Password, secret.Password) {
		t.Fatal("secret changed while held")
	}
	clear(got.Password)
	if _, err := store.Take(secret.ownership()); !errors.Is(err, ErrSessionBrokerSecretMissing) {
		t.Fatalf("second Take error = %v", err)
	}
}

func TestSessionBrokerSecretRejectsMalformedFrames(t *testing.T) {
	secret := SessionBrokerSecret{PoolID: "pool-a", SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-100", Owner: "service-owner", Username: "chuzi-slot-001", Password: []uint16{'s'}}
	frame, err := encodeSessionBrokerSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	frame = append(frame, 0)
	if _, err := decodeSessionBrokerSecret(frame); !errors.Is(err, ErrSessionBrokerSecretInvalid) {
		t.Fatalf("trailing frame byte error = %v", err)
	}
}

func TestSessionBrokerSecretDropUsesExactOwnership(t *testing.T) {
	secret := SessionBrokerSecret{PoolID: "pool-a", SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-100", Owner: "service-owner", Username: "chuzi-slot-001", Password: []uint16{'s', 'e', 'c', 'r', 'e', 't'}, Nonce: [16]byte{1}}
	frame, err := encodeSessionBrokerSecretDrop(sessionBrokerSecretDrop{PoolID: "pool-a", SlotID: secret.SlotID, Ordinal: secret.Ordinal, Generation: secret.Generation, SID: secret.SID, Owner: secret.Owner, Nonce: secret.Nonce})
	if err != nil {
		t.Fatal(err)
	}
	drop, err := decodeSessionBrokerSecretDrop(frame)
	if err != nil || drop.ownership() != secret.ownership() {
		t.Fatalf("drop round trip = %#v, err=%v", drop, err)
	}
	store := NewSessionBrokerSecretStore()
	if err := store.Put(secret); err != nil {
		t.Fatal(err)
	}
	stale := secret.ownership()
	stale.Generation++
	if err := store.DropOwnership(stale, secret.Nonce); !errors.Is(err, ErrSessionBrokerSecretInvalid) {
		t.Fatalf("stale drop error = %v", err)
	}
	if err := store.DropOwnership(secret.ownership(), secret.Nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Take(secret.ownership()); !errors.Is(err, ErrSessionBrokerSecretMissing) {
		t.Fatalf("dropped secret Take error = %v", err)
	}
}
