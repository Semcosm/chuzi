package slotwindows

import (
	"errors"
	"reflect"
	"testing"
)

func TestRunOrderedCleanupUsesRequiredOrder(t *testing.T) {
	var got []string
	callbacks := cleanupOrderCallbacks{
		revokeCapabilities: func() error { got = append(got, "capabilities"); return nil },
		revokeProfile:      func() error { got = append(got, "profile"); return nil },
		stopAgent:          func() error { got = append(got, "agent"); return nil },
		stopSession:        func() error { got = append(got, "session"); return nil },
	}
	if err := runOrderedCleanup("begin", callbacks); err != nil {
		t.Fatal(err)
	}
	want := []string{"capabilities", "profile", "agent", "session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup order = %v, want %v", got, want)
	}
}

func TestRunOrderedCleanupResumesAfterDurableCheckpoint(t *testing.T) {
	var got []string
	callbacks := cleanupOrderCallbacks{
		revokeCapabilities: func() error { got = append(got, "capabilities"); return nil },
		revokeProfile:      func() error { got = append(got, "profile"); return nil },
		stopAgent:          func() error { got = append(got, "agent"); return nil },
		stopSession:        func() error { got = append(got, "session"); return nil },
	}
	if err := runOrderedCleanup("profile_revoked", callbacks); err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resumed cleanup order = %v, want %v", got, want)
	}
}

func TestRunOrderedCleanupReplaysAmbiguousLegacyCheckpoints(t *testing.T) {
	for _, completed := range []string{"agent_stopped", "session_stopped"} {
		t.Run(completed, func(t *testing.T) {
			var got []string
			err := runOrderedCleanup(completed, cleanupOrderCallbacks{
				revokeCapabilities: func() error { got = append(got, "capabilities"); return nil },
				revokeProfile:      func() error { got = append(got, "profile"); return nil },
				stopAgent:          func() error { got = append(got, "agent"); return nil },
				stopSession:        func() error { got = append(got, "session"); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"profile", "agent", "session"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("resumed cleanup order = %v, want %v", got, want)
			}
		})
	}
}

func TestRunOrderedCleanupDoesNotRevisitRemovedProfile(t *testing.T) {
	for _, completed := range []string{"wts_verified", "control_plane_revoked", "profile_deleted"} {
		t.Run(completed, func(t *testing.T) {
			called := false
			err := runOrderedCleanup(completed, cleanupOrderCallbacks{
				revokeCapabilities: func() error { called = true; return nil },
				revokeProfile:      func() error { called = true; return nil },
				stopAgent:          func() error { called = true; return nil },
				stopSession:        func() error { called = true; return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if called {
				t.Fatal("completed runtime cleanup was replayed")
			}
		})
	}
}

func TestRunOrderedCleanupFailsFast(t *testing.T) {
	wantErr := errors.New("profile revoke failed")
	var got []string
	err := runOrderedCleanup("begin", cleanupOrderCallbacks{
		revokeCapabilities: func() error { got = append(got, "capabilities"); return nil },
		revokeProfile:      func() error { got = append(got, "profile"); return wantErr },
		stopAgent:          func() error { got = append(got, "agent"); return nil },
		stopSession:        func() error { got = append(got, "session"); return nil },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(got, []string{"capabilities", "profile"}) {
		t.Fatalf("callbacks after failure = %v", got)
	}
}
