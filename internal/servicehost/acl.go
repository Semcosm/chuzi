package servicehost

import (
	"context"
	"path/filepath"
	"strings"
)

// ACLExpectation is the service/install handoff for effective Windows ACLs.
// Principals are stable role names; the installer resolves them to SIDs and
// verifies the resulting DACL. No caller may replace these paths or roles.
type ACLExpectation struct {
	Name          string
	Path          string
	Owner         string
	ReadExecute   []string
	Modify        []string
	DenyWrite     []string
	NoInheritance bool
}

const (
	ACLSystem         = "system"
	ACLAdministrators = "administrators"
	ACLService        = "chuzi_service"
	ACLSlotUser       = "slot_user"
	ACLEveryone       = "everyone"
	ACLUsers          = "users"
)

// ACLExpectationValidator is implemented by the Windows installer/preflight
// adapter. It must inspect effective DACLs, ownership, and inheritance; a
// mere mode-bit or parent-directory check is insufficient.
type ACLExpectationValidator interface {
	ValidateACL(context.Context, []ACLExpectation) error
}

// BuildACLExpectations returns the fixed ACL tree shared by servicehost and
// installer. Runtime binaries are read/execute only; mutable data and
// credentials are service-owned; slot Profiles are writable only by the
// service and their authenticated slot user. Protected allow lists leave
// Everyone without access; broad deny ACEs are avoided because they would
// also deny a managed slot account through its Users group membership.
func BuildACLExpectations(runtime TrustedRuntime, dataRoot string) ([]ACLExpectation, error) {
	if runtime.Validate() != nil || strings.TrimSpace(dataRoot) == "" || filepath.Clean(dataRoot) != dataRoot || !filepath.IsAbs(dataRoot) {
		return nil, ErrInvalidConfig
	}
	dataRoot = filepath.Clean(dataRoot)
	runtimeRoot := filepath.Clean(runtime.Root)
	if strings.EqualFold(runtimeRoot, dataRoot) || within(runtimeRoot, dataRoot) || within(dataRoot, runtimeRoot) {
		return nil, ErrInvalidConfig
	}
	expectations := []ACLExpectation{
		{Name: "runtime", Path: runtimeRoot, Owner: ACLSystem, ReadExecute: []string{ACLSystem, ACLAdministrators, ACLService}, DenyWrite: []string{ACLUsers}, NoInheritance: true},
		{Name: "data", Path: dataRoot, Owner: ACLSystem, Modify: []string{ACLSystem, ACLAdministrators, ACLService}, DenyWrite: []string{ACLUsers}, NoInheritance: true},
		{Name: "config", Path: filepath.Join(dataRoot, "core-config.json"), Owner: ACLSystem, Modify: []string{ACLSystem, ACLAdministrators, ACLService}, DenyWrite: []string{ACLUsers}, NoInheritance: true},
		{Name: "credentials", Path: filepath.Join(dataRoot, "credentials"), Owner: ACLSystem, Modify: []string{ACLSystem, ACLService}, DenyWrite: []string{ACLUsers}, NoInheritance: true},
		{Name: "profiles", Path: filepath.Join(dataRoot, "Profiles"), Owner: ACLSystem, Modify: []string{ACLSystem, ACLService, ACLSlotUser}, NoInheritance: true},
	}
	if err := ValidateACLExpectations(expectations); err != nil {
		return nil, err
	}
	return expectations, nil
}

func ValidateACLExpectations(expectations []ACLExpectation) error {
	if len(expectations) == 0 {
		return ErrInvalidConfig
	}
	seen := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if strings.TrimSpace(expectation.Name) == "" || strings.TrimSpace(expectation.Path) == "" || filepath.Clean(expectation.Path) != expectation.Path || !filepath.IsAbs(expectation.Path) || strings.TrimSpace(expectation.Owner) == "" {
			return ErrInvalidConfig
		}
		if _, ok := seen[expectation.Name]; ok {
			return ErrInvalidConfig
		}
		seen[expectation.Name] = struct{}{}
		for _, principal := range append(append(append([]string{}, expectation.ReadExecute...), expectation.Modify...), expectation.DenyWrite...) {
			if strings.TrimSpace(principal) == "" || (principal == ACLUsers || principal == ACLEveryone) && (contains(expectation.Modify, principal) || contains(expectation.ReadExecute, principal)) {
				return ErrInvalidConfig
			}
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
