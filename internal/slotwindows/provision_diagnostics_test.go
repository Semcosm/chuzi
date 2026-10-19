package slotwindows

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestProvisionFailureDiagnosticKeepsStageAndNumericCodeOnly(t *testing.T) {
	const privateDetail = `C:\Users\private\NTUSER.DAT password=secret`
	cause := withProvisionNativeError("profile_create", "hresult", 0x80070005, errors.New(privateDetail))
	diagnostic := provisionFailureDiagnostic("slot-private-id", "provision", cause)
	if diagnostic.Resource != "slot-private-id" || diagnostic.Stage != "profile_create" || diagnostic.ErrorClass != "provision_failed" || diagnostic.NativeErrorKind != "hresult" || diagnostic.NativeErrorCode != 0x80070005 {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	serialized := diagnostic.Resource + diagnostic.Stage + diagnostic.ErrorClass + diagnostic.NativeErrorKind
	if strings.Contains(serialized, privateDetail) || strings.Contains(serialized, "secret") {
		t.Fatalf("diagnostic contains raw native error text: %#v", diagnostic)
	}
}

func TestProvisionFailureDiagnosticExtractsWin32CodeThroughStage(t *testing.T) {
	diagnostic := provisionFailureDiagnostic("slot-1", "password_recovery", withProvisionStage("password_reset", syscall.Errno(1326)))
	if diagnostic.Stage != "password_reset" || diagnostic.NativeErrorKind != "win32" || diagnostic.NativeErrorCode != 1326 {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
}
