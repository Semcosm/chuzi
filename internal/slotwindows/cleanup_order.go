package slotwindows

// cleanupOrderCallbacks contains the four externally visible cleanup
// boundaries shared by Stop and Retire. Each callback owns its durable
// checkpoint/journal write so a crash can resume at the last completed step.
type cleanupOrderCallbacks struct {
	revokeCapabilities func() error
	revokeProfile      func() error
	stopAgent          func() error
	stopSession        func() error
}

// runOrderedCleanup executes cleanup in the required safety order and stops at
// the first failed boundary. completed is the durable cleanup step from the
// ownership record; completed boundaries are skipped during recovery.
func runOrderedCleanup(completed string, callbacks cleanupOrderCallbacks) error {
	steps := []struct {
		name string
		fn   func() error
	}{
		{name: "capabilities_revoked", fn: callbacks.revokeCapabilities},
		{name: "profile_revoked", fn: callbacks.revokeProfile},
		{name: "agent_stopped", fn: callbacks.stopAgent},
		{name: "session_stopped", fn: callbacks.stopSession},
	}
	completedRank := cleanupStepRank(completed)
	for index, step := range steps {
		if index+1 <= completedRank || step.fn == nil {
			continue
		}
		if err := step.fn(); err != nil {
			return err
		}
	}
	return nil
}

func cleanupStepRank(step string) int {
	switch step {
	case "capabilities_revoked":
		return 1
	case "profile_revoked":
		return 2
	case "agent_stopped", "session_stopped":
		// These labels were written by the former capability -> Agent ->
		// session -> Profile order. Re-run the idempotent Profile, Agent,
		// and session boundaries so either journal order resumes safely.
		return 1
	case "wts_verified", "control_plane_revoked", "profile_deleted", "root_deleted", "user_deleted":
		return 4
	default:
		return 0
	}
}
