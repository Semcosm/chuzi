package account

import (
	"errors"
	"fmt"
	"time"
)

// FailureClass classifies a failed attempt for retry policy purposes.
type FailureClass string

const (
	TransientFailure     FailureClass = "transient"
	CredentialFailure    FailureClass = "credential"
	PermissionFailure    FailureClass = "permission"
	ConfigurationFailure FailureClass = "configuration"
	UnknownFailure       FailureClass = "unknown"
)

var (
	ErrInvalidRetryPolicy = errors.New("account: invalid retry policy")
	ErrInvalidFailure     = errors.New("account: invalid failure")
)

// Failure is the result of one numbered attempt. Attempt is one-based.
type Failure struct {
	Class   FailureClass
	Attempt int
}

// RetryPolicy bounds retries and exponential backoff. MaxAttempts includes
// the initial attempt, so MaxAttempts=1 disables retry.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// RetryDecision is deterministic output for the queue layer.
type RetryDecision struct {
	Retry bool
	Delay time.Duration
}

func (p RetryPolicy) validate() error {
	if p.MaxAttempts < 1 || p.BaseDelay < 0 || p.MaxDelay < p.BaseDelay {
		return ErrInvalidRetryPolicy
	}
	return nil
}

func (f Failure) validate() error {
	if f.Attempt < 1 {
		return ErrInvalidFailure
	}
	switch f.Class {
	case TransientFailure, CredentialFailure, PermissionFailure,
		ConfigurationFailure, UnknownFailure:
		return nil
	default:
		return fmt.Errorf("%w: class %q", ErrInvalidFailure, f.Class)
	}
}

// Decide returns whether the failure can be retried and its capped delay.
func (p RetryPolicy) Decide(failure Failure) (RetryDecision, error) {
	if err := p.validate(); err != nil {
		return RetryDecision{}, err
	}
	if err := failure.validate(); err != nil {
		return RetryDecision{}, err
	}
	if failure.Class != TransientFailure || failure.Attempt >= p.MaxAttempts {
		return RetryDecision{}, nil
	}

	delay := p.BaseDelay
	if delay == 0 {
		return RetryDecision{Retry: true}, nil
	}
	for step := 1; step < failure.Attempt && delay < p.MaxDelay; step++ {
		if delay > p.MaxDelay/2 {
			delay = p.MaxDelay
			break
		}
		delay *= 2
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	return RetryDecision{Retry: true, Delay: delay}, nil
}
