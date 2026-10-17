//go:build windows

package slotwindows

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// ProtectSessionBrokerStateRoot replaces inherited permissions with the
// narrow ACL required for broker-owned staging and its restart fence. Go's
// Unix-style mode bits do not establish a Windows DACL, so callers must fail
// closed if this operation cannot be applied and verified.
func ProtectSessionBrokerStateRoot(path, brokerSID string) error {
	if path == "" || filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return ErrSessionBrokerInvalidRequest
	}
	if err := inspectNoReparseChain(path); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	if err := (SessionBrokerACL{ServiceSID: "S-1-5-18", BrokerSID: brokerSID}).Validate(); err != nil {
		return ErrSessionBrokerInvalidRequest
	}
	sid, err := windows.StringToSid(brokerSID)
	if err != nil {
		return ErrSessionBrokerInvalidRequest
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid.String() + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return ErrSessionBrokerUnknownOwnership
	}
	if err := windows.SetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation, nil, nil, dacl, nil); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	check, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil || check == nil {
		return ErrSessionBrokerUnknownOwnership
	}
	actual := check.String()
	if strings.Contains(actual, ";;;WD)") || strings.Contains(actual, ";;;BU)") || !strings.Contains(actual, ";;;"+sid.String()+")") {
		return ErrSessionBrokerUnknownOwnership
	}
	return nil
}

func validateSessionBrokerJournalPath(path string) error {
	if inspectNoReparseChain(filepath.Dir(filepath.Clean(path))) != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	attrs, err := windows.GetFileAttributes(wide)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return ErrSessionBrokerUnknownOwnership
	}
	return nil
}

func replaceSessionBrokerJournal(source, target string) error {
	sourceWide, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetWide, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourceWide, targetWide, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
