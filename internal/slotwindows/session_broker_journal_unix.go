//go:build !windows

package slotwindows

import "os"

func validateSessionBrokerJournalPath(string) error { return nil }

func replaceSessionBrokerJournal(source, target string) error {
	return os.Rename(source, target)
}
