package account

import (
	"errors"
	"testing"
	"time"
)

func TestRetryDecisionUsesCappedExponentialBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 4,
		BaseDelay:   5 * time.Second,
		MaxDelay:    12 * time.Second,
	}
	tests := []struct {
		name    string
		failure Failure
		want    RetryDecision
	}{
		{"first-transient", Failure{Class: TransientFailure, Attempt: 1}, RetryDecision{Retry: true, Delay: 5 * time.Second}},
		{"second-transient", Failure{Class: TransientFailure, Attempt: 2}, RetryDecision{Retry: true, Delay: 10 * time.Second}},
		{"capped-transient", Failure{Class: TransientFailure, Attempt: 3}, RetryDecision{Retry: true, Delay: 12 * time.Second}},
		{"max-attempts", Failure{Class: TransientFailure, Attempt: 4}, RetryDecision{}},
		{"credential", Failure{Class: CredentialFailure, Attempt: 1}, RetryDecision{}},
		{"permission", Failure{Class: PermissionFailure, Attempt: 1}, RetryDecision{}},
		{"configuration", Failure{Class: ConfigurationFailure, Attempt: 1}, RetryDecision{}},
		{"unknown", Failure{Class: UnknownFailure, Attempt: 1}, RetryDecision{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := policy.Decide(test.failure)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("decision = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRetryDecisionValidatesInputsAndZeroDelay(t *testing.T) {
	invalidPolicies := []RetryPolicy{
		{MaxAttempts: 0, BaseDelay: time.Second, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: -time.Second, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: 2 * time.Second, MaxDelay: time.Second},
	}
	for _, policy := range invalidPolicies {
		if _, err := policy.Decide(Failure{Class: TransientFailure, Attempt: 1}); !errors.Is(err, ErrInvalidRetryPolicy) {
			t.Errorf("policy %#v error = %v, want ErrInvalidRetryPolicy", policy, err)
		}
	}

	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 0, MaxDelay: 0}
	decision, err := policy.Decide(Failure{Class: TransientFailure, Attempt: 1})
	if err != nil || decision != (RetryDecision{Retry: true}) {
		t.Fatalf("zero-delay decision = %#v, error = %v", decision, err)
	}

	invalidFailures := []Failure{
		{Class: TransientFailure, Attempt: 0},
		{Class: FailureClass("other"), Attempt: 1},
	}
	for _, failure := range invalidFailures {
		if _, err := policy.Decide(failure); !errors.Is(err, ErrInvalidFailure) {
			t.Errorf("failure %#v error = %v, want ErrInvalidFailure", failure, err)
		}
	}
}

func TestRetryDecisionCapsBeforeDurationOverflow(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Duration(1 << 62),
		MaxDelay:    time.Duration(1<<63 - 1),
	}
	decision, err := policy.Decide(Failure{Class: TransientFailure, Attempt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delay != policy.MaxDelay || !decision.Retry {
		t.Fatalf("overflow-safe decision = %#v, want retry capped at %s", decision, policy.MaxDelay)
	}
}
