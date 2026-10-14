package slotwindows

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
)

// SessionBrokerSecretPipeName is a private, OS-authenticated handoff. It is
// deliberately separate from the public broker JSON pipe; credentials never
// enter the broker protocol, journal, logs, argv, or environment.
const SessionBrokerSecretPipeName = "\\\\.\\pipe\\chuzi-session-secret-v1"

const (
	sessionBrokerSecretVersion  = 2
	sessionBrokerSecretMaxFrame = 16 * 1024
	sessionBrokerSecretTTL      = 30 * time.Second
)

const sessionBrokerSecretDropMagic = "CHUZDRP1"

var (
	ErrSessionBrokerSecretInvalid = errors.New("slotwindows: invalid session broker secret")
	ErrSessionBrokerSecretBusy    = errors.New("slotwindows: session broker secret busy")
	ErrSessionBrokerSecretMissing = errors.New("slotwindows: session broker secret missing")
)

// SessionBrokerSecret is confined to the private handoff. Password is a
// temporary UTF-16 buffer and is wiped by Put/Take callers on every path.
type SessionBrokerSecret struct {
	PoolID     string
	SlotID     string
	Ordinal    int
	Generation uint64
	SID        string
	Owner      string
	Username   string   `json:"-"`
	Password   []uint16 `json:"-"`
	Nonce      [16]byte `json:"-"`
}

func (s SessionBrokerSecret) validate() error {
	if !slotIDPattern.MatchString(s.PoolID) || !slotIDPattern.MatchString(s.SlotID) || s.Ordinal < 1 || s.Ordinal > 256 || s.Generation == 0 || !managedSIDPattern.MatchString(s.SID) || !ownerPattern.MatchString(s.Owner) || s.Username == "" || len(s.Username) > 128 || strings.ContainsAny(s.Username, "\r\n\x00") || len(s.Password) == 0 || len(s.Password) > 1024 {
		return ErrSessionBrokerSecretInvalid
	}
	return nil
}

func (s SessionBrokerSecret) ownership() SessionBrokerOwnership {
	return SessionBrokerOwnership{PoolID: s.PoolID, SlotID: s.SlotID, Ordinal: s.Ordinal, Generation: s.Generation, SID: s.SID, Owner: s.Owner}
}

// SessionBrokerSecretStore is process-memory only. Entries expire quickly and
// are deleted when consumed, rejected, or explicitly dropped.
type SessionBrokerSecretStore struct {
	mu      sync.Mutex
	entries map[string]storedSessionBrokerSecret
}

type storedSessionBrokerSecret struct {
	secret  SessionBrokerSecret
	expires time.Time
}

func NewSessionBrokerSecretStore() *SessionBrokerSecretStore {
	return &SessionBrokerSecretStore{entries: make(map[string]storedSessionBrokerSecret)}
}

func (s *SessionBrokerSecretStore) Put(secret SessionBrokerSecret) error {
	if s == nil || secret.validate() != nil {
		return ErrSessionBrokerSecretInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	if _, exists := s.entries[secret.SlotID]; exists {
		return ErrSessionBrokerSecretBusy
	}
	copySecret := secret
	if copySecret.Nonce == [16]byte{} {
		if _, err := rand.Read(copySecret.Nonce[:]); err != nil {
			return ErrSessionBrokerSecretInvalid
		}
	}
	copySecret.Password = append([]uint16(nil), secret.Password...)
	s.entries[secret.SlotID] = storedSessionBrokerSecret{secret: copySecret, expires: time.Now().Add(sessionBrokerSecretTTL)}
	return nil
}

func (s *SessionBrokerSecretStore) Take(ownership SessionBrokerOwnership) (SessionBrokerSecret, error) {
	if s == nil || validateJournalOwnership(ownership, false) != nil {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	entry, ok := s.entries[ownership.SlotID]
	if !ok {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretMissing
	}
	if !entry.secret.ownership().sameRequest(SessionBrokerRequest{PoolID: ownership.PoolID, SlotID: ownership.SlotID, Ordinal: ownership.Ordinal, Generation: ownership.Generation, SID: ownership.SID, Owner: ownership.Owner}) {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	delete(s.entries, ownership.SlotID)
	return entry.secret, nil
}

func (s *SessionBrokerSecretStore) Drop(slotID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if entry, ok := s.entries[slotID]; ok {
		clear(entry.secret.Password)
		delete(s.entries, slotID)
	}
	s.mu.Unlock()
}

// DropOwnership removes only the pending secret for this exact slot fence and
// nonce. A stale cancellation cannot delete a credential handed off for a
// newer generation, owner, or retry. Missing entries are already clean and
// succeed.
func (s *SessionBrokerSecretStore) DropOwnership(ownership SessionBrokerOwnership, nonce [16]byte) error {
	if s == nil || validateJournalOwnership(ownership, false) != nil || nonce == [16]byte{} {
		return ErrSessionBrokerSecretInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	entry, ok := s.entries[ownership.SlotID]
	if !ok {
		return nil
	}
	if !entry.secret.ownership().sameRequest(SessionBrokerRequest{PoolID: ownership.PoolID, SlotID: ownership.SlotID, Ordinal: ownership.Ordinal, Generation: ownership.Generation, SID: ownership.SID, Owner: ownership.Owner}) || entry.secret.Nonce != nonce {
		return ErrSessionBrokerSecretInvalid
	}
	clear(entry.secret.Password)
	delete(s.entries, ownership.SlotID)
	return nil
}

func (s *SessionBrokerSecretStore) expireLocked(now time.Time) {
	for slotID, entry := range s.entries {
		if !now.Before(entry.expires) {
			clear(entry.secret.Password)
			delete(s.entries, slotID)
		}
	}
}

func encodeSessionBrokerSecret(secret SessionBrokerSecret) ([]byte, error) {
	if err := secret.validate(); err != nil {
		return nil, err
	}
	if secret.Nonce == [16]byte{} {
		if _, err := rand.Read(secret.Nonce[:]); err != nil {
			return nil, ErrSessionBrokerSecretInvalid
		}
	}
	putString := func(dst *[]byte, value string) error {
		if len(value) > 0xffff {
			return ErrSessionBrokerSecretInvalid
		}
		var length [2]byte
		binary.LittleEndian.PutUint16(length[:], uint16(len(value)))
		*dst = append(*dst, length[:]...)
		*dst = append(*dst, value...)
		return nil
	}
	frame := make([]byte, 0, sessionBrokerSecretMaxFrame)
	frame = append(frame, 'C', 'H', 'U', 'Z', 'S', 'E', 'C', '1')
	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], sessionBrokerSecretVersion)
	frame = append(frame, version[:]...)
	var ordinal [2]byte
	binary.LittleEndian.PutUint16(ordinal[:], uint16(secret.Ordinal))
	frame = append(frame, ordinal[:]...)
	var generation [8]byte
	binary.LittleEndian.PutUint64(generation[:], secret.Generation)
	frame = append(frame, generation[:]...)
	frame = append(frame, secret.Nonce[:]...)
	for _, value := range []string{secret.PoolID, secret.SlotID, secret.SID, secret.Owner, secret.Username} {
		if err := putString(&frame, value); err != nil {
			return nil, err
		}
	}
	if len(secret.Password) > 0xffff {
		return nil, ErrSessionBrokerSecretInvalid
	}
	var passwordLength [2]byte
	binary.LittleEndian.PutUint16(passwordLength[:], uint16(len(secret.Password)))
	frame = append(frame, passwordLength[:]...)
	for _, value := range secret.Password {
		var word [2]byte
		binary.LittleEndian.PutUint16(word[:], value)
		frame = append(frame, word[:]...)
	}
	if len(frame) > sessionBrokerSecretMaxFrame {
		clear(frame)
		return nil, ErrSessionBrokerSecretInvalid
	}
	return frame, nil
}

func decodeSessionBrokerSecret(frame []byte) (SessionBrokerSecret, error) {
	if len(frame) < 8+2+2+8+16 || len(frame) > sessionBrokerSecretMaxFrame || string(frame[:8]) != "CHUZSEC1" {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	offset := 8
	readU16 := func() (uint16, error) {
		if len(frame)-offset < 2 {
			return 0, ErrSessionBrokerSecretInvalid
		}
		value := binary.LittleEndian.Uint16(frame[offset:])
		offset += 2
		return value, nil
	}
	version, err := readU16()
	if err != nil || version != sessionBrokerSecretVersion {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	ordinal, err := readU16()
	if err != nil || len(frame)-offset < 8 {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	generation := binary.LittleEndian.Uint64(frame[offset:])
	offset += 8
	var nonce [16]byte
	copy(nonce[:], frame[offset:offset+len(nonce)])
	offset += len(nonce)
	if nonce == [16]byte{} {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	readString := func() (string, error) {
		length, err := readU16()
		if err != nil || int(length) > len(frame)-offset {
			return "", ErrSessionBrokerSecretInvalid
		}
		value := string(frame[offset : offset+int(length)])
		offset += int(length)
		return value, nil
	}
	poolID, err := readString()
	if err != nil {
		return SessionBrokerSecret{}, err
	}
	slotID, err := readString()
	if err != nil {
		return SessionBrokerSecret{}, err
	}
	sid, err := readString()
	if err != nil {
		return SessionBrokerSecret{}, err
	}
	owner, err := readString()
	if err != nil {
		return SessionBrokerSecret{}, err
	}
	username, err := readString()
	if err != nil {
		return SessionBrokerSecret{}, err
	}
	passwordLength, err := readU16()
	if err != nil || int(passwordLength)*2 != len(frame)-offset {
		return SessionBrokerSecret{}, ErrSessionBrokerSecretInvalid
	}
	password := make([]uint16, passwordLength)
	for i := range password {
		password[i] = binary.LittleEndian.Uint16(frame[offset+i*2:])
	}
	secret := SessionBrokerSecret{PoolID: poolID, SlotID: slotID, Ordinal: int(ordinal), Generation: generation, SID: sid, Owner: owner, Username: username, Password: password, Nonce: nonce}
	if err := secret.validate(); err != nil {
		clear(password)
		return SessionBrokerSecret{}, err
	}
	return secret, nil
}

type sessionBrokerSecretDrop struct {
	PoolID     string
	SlotID     string
	Ordinal    int
	Generation uint64
	SID        string
	Owner      string
	Nonce      [16]byte
}

func (d sessionBrokerSecretDrop) ownership() SessionBrokerOwnership {
	return SessionBrokerOwnership{PoolID: d.PoolID, SlotID: d.SlotID, Ordinal: d.Ordinal, Generation: d.Generation, SID: d.SID, Owner: d.Owner}
}

func (d sessionBrokerSecretDrop) validate() error {
	if validateJournalOwnership(d.ownership(), false) != nil || d.Nonce == [16]byte{} {
		return ErrSessionBrokerSecretInvalid
	}
	return nil
}

func encodeSessionBrokerSecretDrop(drop sessionBrokerSecretDrop) ([]byte, error) {
	if err := drop.validate(); err != nil {
		return nil, err
	}
	frame := make([]byte, 0, 128)
	frame = append(frame, sessionBrokerSecretDropMagic...)
	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], sessionBrokerSecretVersion)
	frame = append(frame, version[:]...)
	var ordinal [2]byte
	binary.LittleEndian.PutUint16(ordinal[:], uint16(drop.Ordinal))
	frame = append(frame, ordinal[:]...)
	var generation [8]byte
	binary.LittleEndian.PutUint64(generation[:], drop.Generation)
	frame = append(frame, generation[:]...)
	frame = append(frame, drop.Nonce[:]...)
	for _, value := range []string{drop.PoolID, drop.SlotID, drop.SID, drop.Owner} {
		if len(value) > 0xffff {
			return nil, ErrSessionBrokerSecretInvalid
		}
		var length [2]byte
		binary.LittleEndian.PutUint16(length[:], uint16(len(value)))
		frame = append(frame, length[:]...)
		frame = append(frame, value...)
	}
	if len(frame) > sessionBrokerSecretMaxFrame {
		return nil, ErrSessionBrokerSecretInvalid
	}
	return frame, nil
}

func decodeSessionBrokerSecretDrop(frame []byte) (sessionBrokerSecretDrop, error) {
	if len(frame) < len(sessionBrokerSecretDropMagic)+2+2+8+16 || string(frame[:len(sessionBrokerSecretDropMagic)]) != sessionBrokerSecretDropMagic {
		return sessionBrokerSecretDrop{}, ErrSessionBrokerSecretInvalid
	}
	offset := len(sessionBrokerSecretDropMagic)
	readU16 := func() (uint16, error) {
		if len(frame)-offset < 2 {
			return 0, ErrSessionBrokerSecretInvalid
		}
		value := binary.LittleEndian.Uint16(frame[offset:])
		offset += 2
		return value, nil
	}
	version, err := readU16()
	if err != nil || version != sessionBrokerSecretVersion {
		return sessionBrokerSecretDrop{}, ErrSessionBrokerSecretInvalid
	}
	ordinal, err := readU16()
	if err != nil || len(frame)-offset < 8 {
		return sessionBrokerSecretDrop{}, ErrSessionBrokerSecretInvalid
	}
	generation := binary.LittleEndian.Uint64(frame[offset:])
	offset += 8
	var nonce [16]byte
	copy(nonce[:], frame[offset:offset+len(nonce)])
	offset += len(nonce)
	readString := func() (string, error) {
		length, err := readU16()
		if err != nil || int(length) > len(frame)-offset {
			return "", ErrSessionBrokerSecretInvalid
		}
		value := string(frame[offset : offset+int(length)])
		offset += int(length)
		return value, nil
	}
	poolID, err := readString()
	if err != nil {
		return sessionBrokerSecretDrop{}, err
	}
	slotID, err := readString()
	if err != nil {
		return sessionBrokerSecretDrop{}, err
	}
	sid, err := readString()
	if err != nil {
		return sessionBrokerSecretDrop{}, err
	}
	owner, err := readString()
	if err != nil || offset != len(frame) {
		return sessionBrokerSecretDrop{}, ErrSessionBrokerSecretInvalid
	}
	drop := sessionBrokerSecretDrop{PoolID: poolID, SlotID: slotID, Ordinal: int(ordinal), Generation: generation, SID: sid, Owner: owner, Nonce: nonce}
	if err := drop.validate(); err != nil {
		return sessionBrokerSecretDrop{}, err
	}
	return drop, nil
}
