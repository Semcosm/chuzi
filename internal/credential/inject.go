package credential

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	ErrInvalidSource = errors.New("credential: invalid source")
	ErrSourceEmpty   = errors.New("credential: source is empty")
)

// Source supplies one plaintext credential only for the duration of an
// injection. Implementations must not retain or log the returned bytes.
type Source interface {
	Read(context.Context) ([]byte, error)
}

// EnvSource reads a credential from one explicitly configured environment
// variable. The variable name is configuration, never derived from an
// account identifier, to prevent accidental cross-account disclosure.
type EnvSource struct {
	Name string
}

func NewEnvSource(name string) (EnvSource, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "=\x00\r\n") {
		return EnvSource{}, ErrInvalidSource
	}
	return EnvSource{Name: name}, nil
}

func (e EnvSource) Read(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidSource
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	name := strings.TrimSpace(e.Name)
	if name == "" || strings.ContainsAny(name, "=\x00\r\n") {
		return nil, ErrInvalidSource
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil, ErrSourceEmpty
	}
	if len(value) > MaxPayloadSize {
		return nil, fmt.Errorf("%w: payload exceeds limit", ErrInvalidCredential)
	}
	return []byte(value), nil
}

// Inject reads one secret from source and immediately encrypts it through the
// normal Put path. The source buffer is wiped before returning.
func (s *Service) Inject(ctx context.Context, accountID string, actor string, at time.Time, source Source) (Metadata, error) {
	if source == nil {
		return Metadata{}, ErrInvalidSource
	}
	plaintext, err := source.Read(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer clear(plaintext)
	if len(plaintext) == 0 {
		return Metadata{}, ErrSourceEmpty
	}
	return s.Put(ctx, accountID, plaintext, actor, at)
}
