package coretransport

import (
	"testing"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

func TestErrorFromPayloadNormalizesUnknownCode(t *testing.T) {
	err := errorFromPayload(&ErrorPayload{Code: coreapi.Code("future_code"), Message: "must not cross the boundary"})
	if got := coreapi.CodeOf(err); got != coreapi.CodeInternal {
		t.Fatalf("error code = %q, want %q", got, coreapi.CodeInternal)
	}
	if err.Error() != "chuzi core: internal" {
		t.Fatalf("error text = %q, want stable internal classification", err)
	}
}
