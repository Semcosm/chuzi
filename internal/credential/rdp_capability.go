package credential

// This file defines the short-lived authorization handoff for interactive
// RDP. A capability is an opaque bearer token; connection material stays in
// this boundary and is released only through Resolve.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRDPLease = 2 * time.Minute
	MaxRDPLease     = 5 * time.Minute
)

var (
	ErrRDPUnauthorized   = errors.New("credential: RDP authorization failed")
	ErrRDPCapability     = errors.New("credential: invalid RDP capability")
	ErrRDPExpired        = errors.New("credential: RDP capability expired")
	ErrRDPRevoked        = errors.New("credential: RDP capability revoked")
	ErrRDPInvalidRequest = errors.New("credential: invalid RDP capability request")
)

// RDPAuthorization is the minimum context an authorizer needs. It contains no
// endpoint or credential material.
type RDPAuthorization struct {
	AccountID string
	RequestID string
	Actor     string
}

// RDPAuthorizer checks the account/request policy and returns ephemeral
// connection material. Implementations must not persist or log the material.
type RDPAuthorizer interface {
	AuthorizeRDP(context.Context, RDPAuthorization) (RDPMaterial, error)
}

// RDPMaterial is held only by the credential boundary. Callers can obtain a
// copy for the FreeRDP adapter, but it is never part of RDPCapability or JSON.
type RDPMaterial struct {
	host                      string
	port                      uint16
	username                  string
	password                  []byte
	domain                    string
	allowUntrustedCertificate bool
}

func NewRDPMaterial(host string, port uint16, username, password, domain string, allowUntrustedCertificate bool) (RDPMaterial, error) {
	if strings.TrimSpace(host) == "" || port == 0 || strings.TrimSpace(username) == "" || password == "" {
		return RDPMaterial{}, ErrRDPUnauthorized
	}
	return RDPMaterial{host: strings.TrimSpace(host), port: port, username: username, password: []byte(password), domain: domain, allowUntrustedCertificate: allowUntrustedCertificate}, nil
}

func (m RDPMaterial) Host() string                    { return m.host }
func (m RDPMaterial) Port() uint16                    { return m.port }
func (m RDPMaterial) Username() string                { return m.username }
func (m RDPMaterial) Domain() string                  { return m.domain }
func (m RDPMaterial) AllowUntrustedCertificate() bool { return m.allowUntrustedCertificate }
func (m RDPMaterial) PasswordCopy() []byte            { return append([]byte(nil), m.password...) }
func (m *RDPMaterial) wipe() {
	if m == nil {
		return
	}
	clear(m.password)
	m.password = nil
	m.host, m.username, m.domain = "", "", ""
	m.port = 0
	m.allowUntrustedCertificate = false
}

// RDPCapability is safe to pass across the Core/UI boundary. Token is an
// opaque bearer value and carries no endpoint, username, password, profile,
// or certificate information. Use String/LogValue for diagnostics.
type RDPCapability struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	RequestID string    `json:"request_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c RDPCapability) String() string {
	return fmt.Sprintf("rdp-capability{id=%s,request=%s,expires=%s}", c.ID, c.RequestID, c.ExpiresAt.UTC().Format(time.RFC3339))
}
func (c RDPCapability) LogValue() string { return c.String() }
func (c RDPCapability) MarshalLogJSON() []byte {
	b, _ := json.Marshal(struct {
		ID        string    `json:"id"`
		RequestID string    `json:"request_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}{c.ID, c.RequestID, c.ExpiresAt})
	return b
}

// RedactRDPCapability removes the bearer token before structured logging.
func RedactRDPCapability(c RDPCapability) map[string]any {
	return map[string]any{"id": c.ID, "request_id": c.RequestID, "expires_at": c.ExpiresAt.UTC().Format(time.RFC3339)}
}

type RDPService struct {
	authorizer RDPAuthorizer
	mu         sync.Mutex
	entries    map[string]*rdpEntry
}

type rdpEntry struct {
	capability RDPCapability
	material   RDPMaterial
	actor      string
	revoked    bool
}

func NewRDPService(authorizer RDPAuthorizer) (*RDPService, error) {
	if authorizer == nil {
		return nil, ErrInvalidService
	}
	return &RDPService{authorizer: authorizer, entries: make(map[string]*rdpEntry)}, nil
}

func (s *RDPService) Issue(ctx context.Context, request RDPAuthorization, now time.Time, ttl time.Duration) (RDPCapability, error) {
	if err := ctxErr(ctx); err != nil {
		return RDPCapability{}, err
	}
	if err := validateRDPAuthorization(request); err != nil || now.IsZero() {
		return RDPCapability{}, ErrRDPInvalidRequest
	}
	if ttl == 0 {
		ttl = DefaultRDPLease
	}
	if ttl <= 0 || ttl > MaxRDPLease {
		return RDPCapability{}, ErrRDPInvalidRequest
	}
	material, err := s.authorizer.AuthorizeRDP(ctx, request)
	if err != nil {
		material.wipe()
		return RDPCapability{}, ErrRDPUnauthorized
	}
	if material.host == "" || len(material.password) == 0 {
		material.wipe()
		return RDPCapability{}, ErrRDPUnauthorized
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		material.wipe()
		return RDPCapability{}, ErrRDPUnauthorized
	}
	token := hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	capability := RDPCapability{ID: "rdp_" + hex.EncodeToString(digest[:6]), Token: token, RequestID: request.RequestID, ExpiresAt: now.UTC().Add(ttl)}
	s.mu.Lock()
	s.entries[hex.EncodeToString(digest[:])] = &rdpEntry{capability: capability, material: material, actor: request.Actor}
	s.mu.Unlock()
	return capability, nil
}

func (s *RDPService) Resolve(ctx context.Context, capability RDPCapability, actor string, now time.Time) (RDPMaterial, error) {
	if err := ctxErr(ctx); err != nil {
		return RDPMaterial{}, err
	}
	if strings.TrimSpace(capability.Token) == "" || strings.TrimSpace(actor) == "" || now.IsZero() {
		return RDPMaterial{}, ErrRDPCapability
	}
	digest := sha256.Sum256([]byte(capability.Token))
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[hex.EncodeToString(digest[:])]
	if entry == nil || entry.capability.ID != capability.ID || entry.capability.RequestID != capability.RequestID {
		return RDPMaterial{}, ErrRDPCapability
	}
	if entry.revoked {
		return RDPMaterial{}, ErrRDPRevoked
	}
	if !now.Before(entry.capability.ExpiresAt) {
		entry.material.wipe()
		delete(s.entries, hex.EncodeToString(digest[:]))
		return RDPMaterial{}, ErrRDPExpired
	}
	if entry.actor != actor {
		return RDPMaterial{}, ErrRDPUnauthorized
	}
	return cloneRDPMaterial(entry.material), nil
}

func (s *RDPService) Revoke(ctx context.Context, capability RDPCapability, actor string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(capability.Token))
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[hex.EncodeToString(digest[:])]
	if entry == nil || entry.capability.ID != capability.ID {
		return ErrRDPCapability
	}
	if entry.actor != actor {
		return ErrRDPUnauthorized
	}
	entry.revoked = true
	entry.material.wipe()
	return nil
}

func cloneRDPMaterial(m RDPMaterial) RDPMaterial {
	m.password = append([]byte(nil), m.password...)
	return m
}
func validateRDPAuthorization(v RDPAuthorization) error {
	if strings.TrimSpace(v.AccountID) == "" || strings.TrimSpace(v.RequestID) == "" || strings.TrimSpace(v.Actor) == "" || strings.ContainsAny(v.AccountID+v.RequestID+v.Actor, "\r\n\x00") {
		return ErrRDPInvalidRequest
	}
	return nil
}
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
