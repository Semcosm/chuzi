//go:build windows

package servicehost

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type windowsPreflightSource struct {
	options WindowsPreflightOptions
}

const maxSupportedSessions = 32

func NewWindowsPreflightSource(options WindowsPreflightOptions) (FactSource, error) {
	if err := options.Runtime.Validate(); err != nil || !fixedDataRoot(options.DataRoot) {
		return nil, ErrInvalidConfig
	}
	if len(options.RequiredServices) == 0 {
		options.RequiredServices = []string{"TermService", "RpcSs", "ProfSvc"}
	}
	if options.MaxSessions < 0 || options.MaxSessions > maxSupportedSessions {
		return nil, ErrInvalidConfig
	}
	for _, name := range options.RequiredServices {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\\/\r\n\t") {
			return nil, ErrInvalidConfig
		}
	}
	return windowsPreflightSource{options: options}, nil
}

func (s windowsPreflightSource) Check(ctx context.Context) ([]Fact, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	facts := make([]Fact, 0, len(RequiredFactNames))
	add := func(name string, check func() (FactState, string, string)) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, reason, action := check()
		facts = append(facts, Fact{Name: name, State: state, Reason: boundFactText(reason), Action: boundFactText(action)})
		return nil
	}
	checks := []struct {
		name  string
		check func() (FactState, string, string)
	}{
		{"os_edition", s.osEdition},
		{"session_capacity", s.sessionCapacity},
		{"privilege", s.privilege},
		{"required_services", s.requiredServices},
		{"rdp_listener", s.rdpListener},
		{"nla_policy", s.nlaPolicy},
		{"certificate_policy", s.certificatePolicy},
		{"logon_rights", s.logonRights},
		{"disk_permissions", s.diskPermissions},
		{"runtime_signature", s.runtimeSignature},
		{"helper_availability", s.helperAvailability},
	}
	for _, item := range checks {
		if err := add(item.name, item.check); err != nil {
			return nil, err
		}
	}
	return facts, nil
}

func boundFactText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func (s windowsPreflightSource) osEdition() (FactState, string, string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", registry.QUERY_VALUE)
	if err != nil {
		return FactSkip, "edition_unavailable", "inspect_windows_edition"
	}
	defer key.Close()
	product, _, err := key.GetStringValue("ProductName")
	if err != nil || strings.TrimSpace(product) == "" {
		return FactSkip, "edition_unavailable", "inspect_windows_edition"
	}
	build, _, buildErr := key.GetStringValue("CurrentBuildNumber")
	displayVersion, _, displayErr := key.GetStringValue("DisplayVersion")
	installationType, _, installationErr := key.GetStringValue("InstallationType")
	if buildErr != nil && displayErr != nil {
		return FactSkip, "edition_build_unavailable", "inspect_windows_edition"
	}
	if installationErr != nil {
		installationType = ""
	}
	if !supportedWindowsEdition(product, build, displayVersion, installationType) {
		return FactFail, "unsupported_windows_edition", "install_supported_windows_edition"
	}
	return FactPass, "supported_windows_edition", ""
}

func supportedWindowsEdition(product, build, displayVersion, installationType string) bool {
	product = strings.ToLower(strings.TrimSpace(product))
	buildNumber, buildOK := parseWindowsBuild(build)
	if strings.Contains(product, "windows 11") &&
		(strings.Contains(product, " pro") || strings.Contains(product, " enterprise")) {
		// Some images report the 22H2 base build while DisplayVersion carries
		// the servicing release, so enforce both the build floor and release.
		releaseOK := atLeastWindowsRelease(displayVersion, 23)
		if strings.TrimSpace(displayVersion) == "" {
			releaseOK = buildNumber >= 22631
		}
		return buildOK && buildNumber >= 22621 && releaseOK
	}
	if strings.Contains(product, "windows server 2022") || strings.Contains(product, "windows server 2025") {
		// InstallationType=Server is the Desktop Experience image. Server Core
		// is intentionally rejected because it cannot provide the selected
		// interactive session contract.
		return strings.EqualFold(strings.TrimSpace(installationType), "server") && buildOK
	}
	return false
}

func parseWindowsBuild(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed > 0
}

func atLeastWindowsRelease(value string, year int) bool {
	value = strings.TrimSpace(strings.ToUpper(value))
	if len(value) < 4 || (value[len(value)-2:] != "H1" && value[len(value)-2:] != "H2") {
		return false
	}
	releaseYear, err := strconv.Atoi(value[:len(value)-2])
	return err == nil && releaseYear >= year
}

func (s windowsPreflightSource) sessionCapacity() (FactState, string, string) {
	var raw *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil {
		return FactSkip, "session_inventory_unavailable", "check_terminal_services"
	}
	if raw != nil {
		defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
	}
	maxSessions := s.options.MaxSessions
	if maxSessions == 0 {
		maxSessions = maxSupportedSessions
	}
	if int(count) >= maxSessions {
		return FactFail, "session_capacity_exhausted", "reduce_requested_slots"
	}
	return FactPass, "session_inventory_available", ""
}

func (s windowsPreflightSource) privilege() (FactState, string, string) {
	if err := ValidateServiceToken(ProductionIdentity()); err != nil {
		return FactFail, "service_token_incomplete", "run_control_service_as_localsystem"
	}
	return FactPass, "service_token_valid", ""
}

func (s windowsPreflightSource) requiredServices() (FactState, string, string) {
	m, err := mgr.Connect()
	if err != nil {
		return FactSkip, "service_manager_unavailable", "inspect_required_services"
	}
	defer m.Disconnect()
	for _, name := range s.options.RequiredServices {
		service, openErr := m.OpenService(name)
		if openErr != nil {
			return FactFail, "required_service_missing", "start_required_services"
		}
		status, queryErr := service.Query()
		_ = service.Close()
		if queryErr != nil || status.State != svc.Running {
			return FactFail, "required_service_not_running", "start_required_services"
		}
	}
	return FactPass, "required_services_running", ""
}

func (s windowsPreflightSource) rdpListener() (FactState, string, string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Terminal Server", registry.QUERY_VALUE)
	if err != nil {
		return FactSkip, "rdp_policy_unavailable", "inspect_rdp_policy"
	}
	defer key.Close()
	deny, _, err := key.GetIntegerValue("fDenyTSConnections")
	if err != nil {
		return FactSkip, "rdp_policy_unavailable", "inspect_rdp_policy"
	}
	if deny != 0 {
		return FactFail, "rdp_connections_denied", "enable_rdp_explicitly"
	}
	listenerKey, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp", registry.QUERY_VALUE)
	if err != nil {
		return FactSkip, "rdp_listener_unavailable", "inspect_rdp_listener"
	}
	defer listenerKey.Close()
	port, _, err := listenerKey.GetIntegerValue("PortNumber")
	if err != nil {
		return FactSkip, "rdp_port_unavailable", "inspect_rdp_listener"
	}
	if port == 0 || port > 65535 {
		return FactFail, "rdp_port_invalid", "configure_rdp_listener"
	}
	// Registry policy alone does not prove that TermService has bound the
	// endpoint. A short loopback connect keeps this fact bounded and avoids
	// changing firewall or RDP policy during preflight.
	address := net.JoinHostPort("127.0.0.1", strconv.FormatUint(port, 10))
	connection, dialErr := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if dialErr != nil {
		return FactFail, "rdp_listener_not_active", "start_rdp_listener"
	}
	_ = connection.Close()
	return FactPass, "rdp_connections_allowed", ""
}

func (s windowsPreflightSource) nlaPolicy() (FactState, string, string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp", registry.QUERY_VALUE)
	if err != nil {
		return FactSkip, "nla_policy_unavailable", "inspect_nla_policy"
	}
	defer key.Close()
	nla, _, err := key.GetIntegerValue("UserAuthentication")
	if err != nil {
		return FactSkip, "nla_policy_unavailable", "inspect_nla_policy"
	}
	if nla == 0 {
		return FactFail, "nla_disabled", "enable_nla_explicitly"
	}
	return FactPass, "nla_enabled", ""
}

func (s windowsPreflightSource) certificatePolicy() (FactState, string, string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp", registry.QUERY_VALUE)
	if err != nil {
		return FactSkip, "certificate_policy_unavailable", "inspect_rdp_certificate"
	}
	defer key.Close()
	certificate, _, err := key.GetBinaryValue("SSLCertificateSHA1Hash")
	if err != nil || len(certificate) != 20 {
		return FactFail, "rdp_certificate_missing", "configure_rdp_certificate"
	}
	if err := verifyRDPCertificateChain(certificate); err != nil {
		return FactFail, "rdp_certificate_untrusted", "install_trusted_rdp_certificate"
	}
	return FactPass, "rdp_certificate_trusted", ""
}

func (s windowsPreflightSource) logonRights() (FactState, string, string) {
	if strings.TrimSpace(s.options.LogonSID) == "" {
		// A host can start before the first disposable slot exists. Keep this
		// fact explicit and defer the SID-scoped effective-rights check to the
		// slot provisioning preflight; never substitute group membership.
		return FactPass, "managed_account_check_deferred", "check_effective_logon_rights_before_slot_login"
	}
	if _, err := windows.StringToSid(s.options.LogonSID); err != nil {
		return FactFail, "target_account_invalid", "repair_managed_account"
	}
	hasRight, err := lsaAccountHasRight(s.options.LogonSID, "SeRemoteInteractiveLogonRight")
	if err != nil {
		return FactSkip, "effective_logon_rights_unavailable", "inspect_effective_logon_rights"
	}
	if !hasRight {
		return FactFail, "remote_interactive_right_missing", "grant_remote_interactive_right_explicitly"
	}
	for _, sid := range []string{s.options.LogonSID, "S-1-1-0", "S-1-5-11"} {
		denied, denyErr := lsaAccountHasRight(sid, "SeDenyRemoteInteractiveLogonRight")
		if denyErr != nil {
			return FactSkip, "effective_logon_rights_unavailable", "inspect_effective_logon_rights"
		}
		if denied {
			return FactFail, "remote_interactive_right_denied", "remove_explicit_remote_interactive_deny"
		}
	}
	return FactPass, "remote_interactive_right_granted", ""
}

func (s windowsPreflightSource) diskPermissions() (FactState, string, string) {
	if s.options.ACL != nil {
		expectations, err := BuildACLExpectations(s.options.Runtime, s.options.DataRoot)
		if err != nil || s.options.ACL.ValidateACL(context.Background(), expectations) != nil {
			return FactFail, "effective_acl_invalid", "repair_installation_acl"
		}
	}
	info, err := os.Stat(s.options.DataRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return FactFail, "data_root_unavailable", "repair_data_permissions"
	}
	if file, err := os.Open(s.options.DataRoot); err != nil {
		return FactFail, "data_root_not_readable", "repair_data_permissions"
	} else {
		_ = file.Close()
	}
	probe, err := os.CreateTemp(s.options.DataRoot, ".chuzi-preflight-*")
	if err != nil {
		return FactFail, "data_root_not_writable", "repair_data_permissions"
	}
	probeName := probe.Name()
	defer os.Remove(probeName)
	if _, err := probe.Write([]byte{0}); err != nil {
		_ = probe.Close()
		return FactFail, "data_root_not_writable", "repair_data_permissions"
	}
	if err := probe.Sync(); err != nil {
		_ = probe.Close()
		return FactFail, "data_root_not_durable", "repair_data_permissions"
	}
	if err := probe.Close(); err != nil {
		return FactFail, "data_root_not_writable", "repair_data_permissions"
	}
	return FactPass, "data_root_read_write", ""
}

func (s windowsPreflightSource) runtimeSignature() (FactState, string, string) {
	for _, path := range []string{s.options.Runtime.Core, s.options.Runtime.Broker, s.options.Runtime.Agent, s.options.Runtime.Node, s.options.Runtime.Shell} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return FactFail, "runtime_file_missing", "repair_signed_runtime"
		}
		if err := verifyAuthenticode(path); err != nil {
			return FactFail, "runtime_signature_invalid", "repair_signed_runtime"
		}
	}
	if _, err := os.Stat(filepath.Join(s.options.Runtime.Root, "authenticode-policy.json")); err != nil {
		return FactSkip, "signature_policy_unavailable", "run_installer_signature_validation"
	}
	return FactPass, "runtime_signature_valid", ""
}

func verifyAuthenticode(path string) error {
	filePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fileInfo := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: filePath}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(fileInfo),
		ProvFlags:                       windows.WTD_CACHE_ONLY_URL_RETRIEVAL,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	closeErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	if verifyErr != nil {
		return verifyErr
	}
	return closeErr
}

// verifyRDPCertificateChain resolves the thumbprint selected by the RDP
// listener in the machine certificate store and asks CryptoAPI to validate
// its server chain against the host trust policy. The certificate bytes and
// native status never leave this package.
func verifyRDPCertificateChain(hash []byte) error {
	if len(hash) != 20 {
		return errors.New("invalid certificate thumbprint")
	}
	storeName, err := windows.UTF16PtrFromString("MY")
	if err != nil {
		return err
	}
	store, err := windows.CertOpenSystemStore(0, storeName)
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	blob := windows.CryptHashBlob{Size: uint32(len(hash)), Data: &hash[0]}
	certificate, err := windows.CertFindCertificateInStore(
		store,
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		0,
		windows.CERT_FIND_SHA1_HASH,
		unsafe.Pointer(&blob),
		nil,
	)
	if err != nil || certificate == nil {
		return errors.New("rdp certificate not found")
	}
	defer windows.CertFreeCertificateContext(certificate)
	chainParameters := windows.CertChainPara{Size: uint32(unsafe.Sizeof(windows.CertChainPara{}))}
	var chain *windows.CertChainContext
	if err := windows.CertGetCertificateChain(0, certificate, nil, 0, &chainParameters, 0, 0, &chain); err != nil || chain == nil {
		return errors.New("rdp certificate chain unavailable")
	}
	defer windows.CertFreeCertificateChain(chain)
	extra := windows.SSLExtraCertChainPolicyPara{Size: uint32(unsafe.Sizeof(windows.SSLExtraCertChainPolicyPara{})), AuthType: windows.AUTHTYPE_SERVER}
	policyParameters := windows.CertChainPolicyPara{Size: uint32(unsafe.Sizeof(windows.CertChainPolicyPara{})), ExtraPolicyPara: windows.Pointer(unsafe.Pointer(&extra))}
	policyStatus := windows.CertChainPolicyStatus{Size: uint32(unsafe.Sizeof(windows.CertChainPolicyStatus{}))}
	if err := windows.CertVerifyCertificateChainPolicy(windows.CERT_CHAIN_POLICY_SSL, chain, &policyParameters, &policyStatus); err != nil || policyStatus.Error != 0 {
		return errors.New("rdp certificate chain untrusted")
	}
	return nil
}

type lsaUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type lsaObjectAttributes struct {
	Length                   uint32
	RootDirectory            uintptr
	ObjectName               uintptr
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

func lsaAccountHasRight(sidText, rightName string) (bool, error) {
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return false, err
	}
	attrs := lsaObjectAttributes{Length: uint32(unsafe.Sizeof(lsaObjectAttributes{}))}
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	open := advapi.NewProc("LsaOpenPolicy")
	enumerate := advapi.NewProc("LsaEnumerateAccountRights")
	free := advapi.NewProc("LsaFreeMemory")
	close := advapi.NewProc("LsaClose")
	var policy uintptr
	status, _, _ := open.Call(0, uintptr(unsafe.Pointer(&attrs)), 0x00000800, uintptr(unsafe.Pointer(&policy)))
	if status != 0 || policy == 0 {
		return false, errors.New("lsa policy unavailable")
	}
	defer close.Call(policy)
	var rights *lsaUnicodeString
	var count uint32
	status, _, _ = enumerate.Call(policy, uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&rights)), uintptr(unsafe.Pointer(&count)))
	const (
		statusObjectNameNotFound = 0xC0000034
		statusObjectPathNotFound = 0xC000003A
		statusNoMoreEntries      = 0x8000001A
		statusNoSuchPrivilege    = 0xC0000060
	)
	if status == statusObjectNameNotFound || status == statusObjectPathNotFound || status == statusNoMoreEntries || status == statusNoSuchPrivilege {
		return false, nil
	}
	if status != 0 || rights == nil || count == 0 {
		if status == 0 {
			return false, nil
		}
		return false, errors.New("lsa account rights unavailable")
	}
	defer free.Call(uintptr(unsafe.Pointer(rights)))
	for _, right := range unsafe.Slice(rights, count) {
		if right.Buffer != nil && right.Length > 0 && windows.UTF16ToString(unsafe.Slice(right.Buffer, int(right.Length/2))) == rightName {
			return true, nil
		}
	}
	return false, nil
}

func (s windowsPreflightSource) helperAvailability() (FactState, string, string) {
	if s.options.Helper == nil {
		// The operator helper is registered at interactive logon. Host startup
		// records that registration is deferred; a slot cannot become ready
		// until a subsequent authenticated health check observes it.
		return FactPass, "helper_registration_deferred", "await_authenticated_helper"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := RequireHelper(ctx, s.options.Helper, s.options.Runtime, time.Now().UTC(), 10*time.Second); err != nil {
		if errors.Is(err, ErrHelperMismatch) {
			return FactFail, "helper_identity_mismatch", "restart_authenticated_helper"
		}
		return FactFail, "helper_not_ready", "start_authenticated_helper"
	}
	return FactPass, "helper_ready", ""
}
