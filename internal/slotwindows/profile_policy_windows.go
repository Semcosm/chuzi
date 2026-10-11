//go:build windows

package slotwindows

import (
	"context"
	"crypto/sha1"
	"crypto/x509"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	logonWithProfile                = 0x00000001
	profileCreateUnicodeEnvironment = 0x00000400
	createNoWindow                  = 0x08000000
	regProcessAppKey                = 0x00000001
	keyReadWrite                    = registry.READ | registry.WRITE | registry.CREATE_SUB_KEY
	waitObject0                     = 0x00000000
	waitTimeout                     = 0x00000102
)

var (
	advapi32Profile             = windows.NewLazySystemDLL("advapi32.dll")
	procCreateProcessWithLogonW = advapi32Profile.NewProc("CreateProcessWithLogonW")
	procRegLoadAppKeyW          = advapi32Profile.NewProc("RegLoadAppKeyW")
)

// ApplyProfilePolicy derives the target hive from the managed SID and writes
// only the user's Winlogon value. It never writes HKLM and never accepts a
// caller-selected profile path, executable, or command.
func applyProfilePolicy(ctx context.Context, sid, runtimeRoot string) error {
	if ctx == nil {
		return ErrProfilePolicy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !managedSIDPattern.MatchString(sid) {
		return ErrProfilePolicy
	}
	policy, err := NewProfilePolicy(runtimeRoot)
	if err != nil {
		return err
	}
	command, err := policy.ShellCommand()
	if err != nil {
		return err
	}
	scriptPath := policy.ScriptPath()
	if !runtimeRegularNoReparse(scriptPath) {
		return ErrProfilePolicyPath
	}
	if err := verifySignedSessionShell(scriptPath); err != nil {
		return err
	}
	if err := verifyRuntimeACL(filepath.Dir(scriptPath), sid); err != nil {
		return err
	}
	if err := verifyRuntimeACL(scriptPath, sid); err != nil {
		return err
	}
	profilePath, err := profilePathForSID(sid)
	if err != nil {
		return ErrProfilePolicy
	}
	if err := validateProfileHivePath(profilePath); err != nil {
		return err
	}
	hive, closeHive, err := openUserHive(sid, profilePath)
	if err != nil {
		return ErrProfilePolicyRegistry
	}
	defer closeHive()
	key, _, err := registry.CreateKey(hive, `Software\Microsoft\Windows NT\CurrentVersion\Winlogon`, keyReadWrite)
	if err != nil {
		return ErrProfilePolicyRegistry
	}
	defer key.Close()
	if err := key.SetStringValue("Shell", command); err != nil {
		return ErrProfilePolicyRegistry
	}
	value, _, err := key.GetStringValue("Shell")
	if err != nil || ValidateSessionShellCommand(runtimeRoot, value) != nil {
		return ErrProfilePolicyRegistry
	}
	return nil
}

// ReadProfileShellCommand is used by native smoke to verify the target HKCU
// policy after Winlogon has loaded the profile.
func ReadProfileShellCommand(sid string) (string, error) {
	if !managedSIDPattern.MatchString(sid) {
		return "", ErrProfilePolicy
	}
	key, err := registry.OpenKey(registry.USERS, sid+`\Software\Microsoft\Windows NT\CurrentVersion\Winlogon`, registry.QUERY_VALUE)
	if err != nil {
		return "", ErrProfilePolicyRegistry
	}
	defer key.Close()
	value, _, err := key.GetStringValue("Shell")
	if err != nil {
		return "", ErrProfilePolicyRegistry
	}
	return value, nil
}

func profilePathForSID(sid string) (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+sid, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("ProfileImagePath")
	if err != nil {
		return "", err
	}
	expandSource, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	count, err := windows.ExpandEnvironmentStrings(expandSource, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if count == 0 || count > uint32(len(buffer)) {
		return "", ErrProfilePolicyPath
	}
	return windows.UTF16ToString(buffer[:count]), nil
}

func validateProfileHivePath(path string) error {
	if path == "" || !filepath.IsAbs(path) || hasTraversalComponent(path) || strings.ContainsAny(path, "\x00\r\n") || inspectNoReparseChain(path) != nil {
		return ErrProfilePolicyPath
	}
	ntuser := filepath.Join(path, "NTUSER.DAT")
	if !runtimeRegularNoReparse(ntuser) {
		return ErrProfileBootstrap
	}
	return nil
}

func openUserHive(sid, profilePath string) (registry.Key, func(), error) {
	if loaded, err := registry.OpenKey(registry.USERS, sid, keyReadWrite); err == nil {
		return loaded, func() { _ = loaded.Close() }, nil
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(profilePath, "NTUSER.DAT"))
	if err != nil {
		return 0, func() {}, err
	}
	var handle windows.Handle
	status, _, callErr := procRegLoadAppKeyW.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&handle)), uintptr(keyReadWrite), regProcessAppKey, 0)
	if status != 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return 0, func() {}, callErr
		}
		return 0, func() {}, windows.Errno(status)
	}
	loaded := registry.Key(handle)
	return loaded, func() { _ = loaded.Close() }, nil
}

// bootstrapProfileWithLogon performs the first profile initialization with
// LOGON_WITH_PROFILE. The command is fixed to system32\cmd.exe /c exit; no
// caller text, script, or executable can enter this boundary.
func bootstrapProfileWithLogon(ctx context.Context, username string, password []uint16) error {
	if ctx == nil || username == "" || len(password) == 0 {
		return ErrProfileBootstrap
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return ErrProfileBootstrap
	}
	application, err := windows.UTF16PtrFromString(filepath.Join(systemDir, "cmd.exe"))
	if err != nil {
		return ErrProfileBootstrap
	}
	user, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return ErrProfileBootstrap
	}
	domain, err := windows.UTF16PtrFromString(".")
	if err != nil {
		return ErrProfileBootstrap
	}
	command, err := windows.UTF16FromString("cmd.exe /c exit")
	if err != nil {
		return ErrProfileBootstrap
	}
	working, err := windows.UTF16PtrFromString(systemDir)
	if err != nil {
		return ErrProfileBootstrap
	}
	defer clearUTF16(command)
	var startup windows.StartupInfo
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var info windows.ProcessInformation
	created, _, callErr := procCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(domain)), uintptr(unsafe.Pointer(&password[0])),
		logonWithProfile, uintptr(unsafe.Pointer(application)), uintptr(unsafe.Pointer(&command[0])),
		profileCreateUnicodeEnvironment|createNoWindow, 0, uintptr(unsafe.Pointer(working)), uintptr(unsafe.Pointer(&startup)), uintptr(unsafe.Pointer(&info)))
	if created == 0 {
		_ = callErr
		return ErrProfileBootstrap
	}
	defer windows.CloseHandle(info.Thread)
	defer windows.CloseHandle(info.Process)
	for {
		if err := ctx.Err(); err != nil {
			_ = windows.TerminateProcess(info.Process, 1)
			return err
		}
		state, waitErr := windows.WaitForSingleObject(info.Process, 100)
		if waitErr != nil {
			return ErrProfileBootstrap
		}
		if state == waitObject0 {
			return nil
		}
		if state != waitTimeout {
			return ErrProfileBootstrap
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func clearUTF16(value []uint16) {
	for i := range value {
		value[i] = 0
	}
}

func verifySignedSessionShell(path string) error {
	policy, err := LoadAuthenticodePolicy(filepath.Join(filepath.Dir(path), "authenticode-policy.json"))
	if err != nil || len(policy.AllowedThumbprints) == 0 {
		return ErrProfilePolicySignature
	}
	filePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrProfilePolicyPath
	}
	fileInfo := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: filePath}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(fileInfo),
		// Offline operation uses the cached chain and embedded timestamp. The
		// lifetime flag permits a valid timestamped signature after certificate
		// expiry; the signing gate performs online revocation before publishing.
		ProvFlags: windows.WTD_CACHE_ONLY_URL_RETRIEVAL | windows.WTD_LIFETIME_SIGNING_FLAG,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	closeErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	if verifyErr != nil || closeErr != nil {
		return ErrProfilePolicySignature
	}
	certificates, err := embeddedSignerCertificates(path)
	if err != nil {
		return ErrProfilePolicySignature
	}
	var signer *x509.Certificate
	for _, certificate := range certificates {
		// The embedded store also carries issuer certificates. Only a leaf
		// signing certificate may satisfy the publisher binding; accepting a
		// matching CA in the chain would not bind the signed file to Chuzi.
		if certificate.IsCA {
			continue
		}
		thumbprint := sha1.Sum(certificate.Raw)
		if policy.Allows(fmtThumbprint(thumbprint[:]), certificate.Subject.String()) {
			if signer != nil {
				return ErrProfilePolicySignature
			}
			signer = certificate
		}
	}
	if signer == nil {
		return ErrProfilePolicySignature
	}
	return nil
}

func fmtThumbprint(value []byte) string {
	const hex = "0123456789ABCDEF"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = hex[item>>4]
		result[index*2+1] = hex[item&0x0f]
	}
	return string(result)
}

// embeddedSignerCertificates returns the certificates carried by the PE
// Authenticode PKCS#7 envelope. WinVerifyTrust establishes chain validity;
// this second check binds the leaf to the installation publisher allowlist.
func embeddedSignerCertificates(path string) ([]*x509.Certificate, error) {
	filePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var store windows.Handle
	err = windows.CryptQueryObject(
		windows.CERT_QUERY_OBJECT_FILE,
		unsafe.Pointer(filePath),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED,
		windows.CERT_QUERY_FORMAT_FLAG_BINARY,
		0, nil, nil, nil, &store, nil, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(store, 0)
	var previous *windows.CertContext
	var certificates []*x509.Certificate
	for {
		certificate, enumErr := windows.CertEnumCertificatesInStore(store, previous)
		if certificate == nil {
			if enumErr != nil && enumErr != windows.ERROR_NO_MORE_FILES {
				return nil, enumErr
			}
			break
		}
		previous = certificate
		if certificate.EncodedCert == nil || certificate.Length == 0 {
			continue
		}
		parsed, parseErr := x509.ParseCertificate(unsafe.Slice(certificate.EncodedCert, certificate.Length))
		if parseErr != nil {
			continue
		}
		certificates = append(certificates, parsed)
	}
	if len(certificates) == 0 {
		return nil, ErrProfilePolicySignature
	}
	return certificates, nil
}
