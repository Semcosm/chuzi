package slotwindows

import "errors"

// WTS absence permits cleanup of the recorded broker resources; it does not
// prove that credentials or target reservations have been removed. Callers
// must still run the broker Stop when expectedID is nonzero.
func validateSessionCleanupObservation(expectedID uint32, observed BootstrapSession, err error) error {
	if sessionConfirmedAbsent(observed, err) {
		return nil
	}
	if err != nil && !errors.Is(err, ErrSessionDisconnected) {
		return ErrSessionQuery
	}
	if expectedID == 0 || observed.ID != expectedID || (observed.State != "active" && observed.State != "disconnected") {
		return ErrSessionChanged
	}
	return nil
}
