package slotwindows

import (
	"errors"
	"syscall"
)

// ProvisionFailureDiagnostic is the bounded, redaction-safe detail emitted
// when native provisioning fails. Resource is a service-derived slot ID; the
// recorder must redact it before it leaves the process.
type ProvisionFailureDiagnostic struct {
	Resource        string
	Stage           string
	ErrorClass      string
	NativeErrorKind string
	NativeErrorCode uint32
}

type provisionStageError struct {
	stage string
	cause error
}

func (e *provisionStageError) Error() string { return e.cause.Error() }
func (e *provisionStageError) Unwrap() error { return e.cause }

type provisionNativeError struct {
	stage string
	kind  string
	code  uint32
	cause error
}

func (e *provisionNativeError) Error() string { return e.cause.Error() }
func (e *provisionNativeError) Unwrap() error { return e.cause }

func withProvisionStage(stage string, cause error) error {
	if cause == nil {
		return nil
	}
	return &provisionStageError{stage: stage, cause: cause}
}

func withProvisionNativeError(stage, kind string, code uint32, cause error) error {
	if cause == nil {
		return nil
	}
	return &provisionNativeError{stage: stage, kind: kind, code: code, cause: cause}
}

func withProvisionWin32Error(stage string, nativeErr, cause error) error {
	if code, ok := provisionWin32Code(nativeErr); ok {
		return withProvisionNativeError(stage, "win32", code, cause)
	}
	return withProvisionStage(stage, cause)
}

func provisionWin32Code(err error) (uint32, bool) {
	var code syscall.Errno
	if !errors.As(err, &code) || code == 0 {
		return 0, false
	}
	return uint32(code), true
}

func provisionFailureDiagnostic(resource, fallbackStage string, cause error) ProvisionFailureDiagnostic {
	diagnostic := ProvisionFailureDiagnostic{
		Resource: resource, Stage: fallbackStage, ErrorClass: "provision_failed",
	}
	var native *provisionNativeError
	if errors.As(cause, &native) {
		diagnostic.Stage = native.stage
		diagnostic.NativeErrorKind = native.kind
		diagnostic.NativeErrorCode = native.code
		return diagnostic
	}
	var staged *provisionStageError
	if errors.As(cause, &staged) {
		diagnostic.Stage = staged.stage
	}
	if code, ok := provisionWin32Code(cause); ok {
		diagnostic.NativeErrorKind = "win32"
		diagnostic.NativeErrorCode = code
	}
	return diagnostic
}

func preserveProvisionNativeError(source, classified error) error {
	var native *provisionNativeError
	if errors.As(source, &native) {
		return withProvisionNativeError(native.stage, native.kind, native.code, classified)
	}
	var staged *provisionStageError
	if errors.As(source, &staged) {
		if code, ok := provisionWin32Code(source); ok {
			return withProvisionNativeError(staged.stage, "win32", code, classified)
		}
		return withProvisionStage(staged.stage, classified)
	}
	if code, ok := provisionWin32Code(source); ok {
		return withProvisionNativeError("session_login", "win32", code, classified)
	}
	return classified
}
