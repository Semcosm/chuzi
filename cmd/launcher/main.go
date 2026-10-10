// chuzi-launcher is the small, UI-neutral bootstrap command shipped with a
// nightly package. A future desktop UI can call the same manifest and
// verification contract without owning release or filesystem policy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/launcher"
)

var version = "dev"

func main() {
	manifestPath := flag.String("manifest", "release-manifest.json", "release manifest path")
	installRoot := flag.String("root", ".", "installation root to inspect")
	verify := flag.Bool("verify", false, "verify declared resources under root")
	command := flag.String("command", "show", "launcher command: show, verify, check-update, initialize, initialize-complete, repair, settings, settings-save, core-status, core-start, core-stop, core-call, core-pool-mode-save, job-pool-list, job-pool-get, job-pool-apply, job-pool-scale, job-pool-drain, job-pool-resume, job-pool-delete, job-pool-operation, start-slot-session, slot-session-operation, environment-list, environment-install, environment-upgrade, environment-verify, environment-trust, environment-enable, environment-disable, environment-health, environment-rollback, environment-operation, component-list, component-install, component-remove, component-enable, component-disable, plugin-list, plugin-install, plugin-update, plugin-remove, plugin-enable, plugin-disable, plugin-trust, plugin-untrust")
	sourceRoot := flag.String("source-root", "", "trusted local source root for repair/install")
	updateManifest := flag.String("update-manifest", "", "candidate manifest for check-update")
	releaseIndexURL := flag.String("release-index", "", "HTTPS release index URL for update and component downloads")
	downloadDir := flag.String("download-dir", "", "component archive cache directory (default: <root>/.chuzi/downloads)")
	allowHTTPForLoopback := flag.Bool("allow-http-loopback", false, "allow HTTP release index/artifacts only for localhost test servers")
	currentVersion := flag.String("current-version", "", "installed version for check-update")
	item := flag.String("item", "", "component or plugin id")
	paths := flag.String("paths", "", "comma-separated resource paths for repair (default: all)")
	trustedSigners := flag.String("trusted-signers", "", "comma-separated plugin signer allowlist")
	settingsPath := flag.String("settings-path", "", "launcher settings path (default: <root>/.chuzi/launcher-settings.json)")
	settingsInput := flag.String("settings-input", "", "JSON file for settings-save")
	coreMethod := flag.String("core-method", "", "Core API method for core-call")
	coreParamsJSON := flag.String("core-params-json", "{}", "JSON parameters for core-call")
	jobPoolInput := flag.String("job-pool-input", "", "typed JSON file for job-pool-apply")
	poolID := flag.String("pool-id", "", "job pool identifier")
	poolMode := flag.String("pool-mode", "", "execution mode for core-pool-mode-save: logical or windows")
	desiredSlots := flag.Int("desired-slots", -1, "desired job pool slots")
	expectedRevision := flag.Uint64("expected-revision", 0, "expected config revision")
	idempotencyKey := flag.String("idempotency-key", "", "idempotency key for control operation")
	actor := flag.String("actor", "", "operator actor identifier")
	operationID := flag.String("operation-id", "", "operation identifier")
	environmentID := flag.String("environment-id", "", "environment identifier")
	environmentVersion := flag.String("environment-version", "", "environment version")
	packageRef := flag.String("package-ref", "", "controlled package reference")
	lockPath := flag.String("lock-path", "", "launcher mutation lock path (default: <root>/.chuzi/launcher.lock)")
	progress := flag.Bool("progress", false, "write operation progress to stderr")
	allowRequiredRemoval := flag.Bool("allow-required-removal", false, "allow removal of required components (only for explicit Core uninstall)")
	showVersion := flag.Bool("version", false, "print launcher version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var manifest launcher.ReleaseManifest
	var installedManifest *launcher.ReleaseManifest
	var releaseIndex *launcher.ReleaseIndex
	manifestRequired := launcherCommandNeedsManifest(*command, *verify)
	if strings.TrimSpace(*releaseIndexURL) != "" {
		source := launcher.HTTPReleaseIndexSource{URL: *releaseIndexURL, AllowHTTPForLoopback: *allowHTTPForLoopback}
		index, err := source.FetchIndex(ctx)
		if err != nil {
			fatal(err)
		}
		if expected := runtimeTarget(); expected != "" && index.Target != expected {
			fatal(fmt.Errorf("release index target %q does not match launcher target %q", index.Target, expected))
		}
		releaseIndex = &index
		manifest = index.Manifest
	}
	data, manifestErr := os.ReadFile(*manifestPath)
	if manifestErr == nil {
		var local launcher.ReleaseManifest
		if err := json.Unmarshal(data, &local); err != nil {
			fatal(err)
		}
		if err := local.Validate(); err != nil {
			fatal(err)
		}
		// An index describes the candidate release, so an installed manifest may
		// legitimately have an older version or commit during an upgrade.
		if releaseIndex != nil && local.Target != manifest.Target {
			fatal(fmt.Errorf("local manifest target does not match release index"))
		}
		if releaseIndex == nil {
			manifest = local
		}
		installedManifest = &local
	} else if releaseIndex == nil && manifestRequired {
		fatal(manifestErr)
	}

	if *verify || *command == "verify" {
		root, err := filepath.Abs(*installRoot)
		if err != nil {
			fatal(err)
		}
		result, err := (launcher.FileVerifier{}).Verify(ctx, root, manifest)
		if err != nil {
			fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fatal(err)
		}
		if !result.Valid {
			os.Exit(1)
		}
		return
	}
	root, err := filepath.Abs(*installRoot)
	if err != nil {
		fatal(err)
	}
	switch *command {
	case "show":
		// Fall through to the stable manifest JSON output below.
	case "check-update":
		current := strings.TrimSpace(*currentVersion)
		if current == "" {
			if installedManifest == nil {
				fatal(fmt.Errorf("-current-version is required when the installed manifest is unavailable"))
			}
			current = installedManifest.Version
		}
		var source launcher.ManifestSource
		if releaseIndex != nil {
			source = launcher.StaticManifestSource{Manifest: releaseIndex.Manifest}
		} else {
			if strings.TrimSpace(*updateManifest) == "" {
				fatal(fmt.Errorf("-update-manifest is required when -release-index is not set"))
			}
			source = launcher.FileManifestSource{Path: *updateManifest}
		}
		result, err := (launcher.ManifestUpdateChecker{Source: source}).Check(ctx, launcher.UpdateRequest{CurrentVersion: current, Target: manifest.Target, Channel: manifest.Channel})
		if err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	case "repair":
		lock, err := acquireMutationLock(ctx, root, *lockPath)
		if err != nil {
			fatal(err)
		}
		source, err := resolveSourceRoot(root, *sourceRoot)
		if err != nil {
			_ = lock.Release()
			fatal(err)
		}
		request := launcher.RepairRequest{InstallRoot: root, Manifest: manifest}
		if strings.TrimSpace(*paths) != "" {
			for _, value := range strings.Split(*paths, ",") {
				if value = strings.TrimSpace(value); value != "" {
					request.Paths = append(request.Paths, value)
				}
			}
		}
		result, err := (launcher.FileRepairer{SourceRoot: source, Progress: progressReporter(*progress)}).Repair(ctx, request)
		if err != nil {
			_ = lock.Release()
			fatal(err)
		}
		if err := lock.Release(); err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	case "initialize", "initialize-complete":
		source, err := resolveSourceRoot(root, *sourceRoot)
		if err != nil {
			fatal(err)
		}
		lock, err := acquireMutationLock(ctx, root, *lockPath)
		if err != nil {
			fatal(err)
		}
		options := launcher.ManagerOptions{InstallRoot: root, SourceRoot: source, Manifest: manifest, Trust: launcher.PluginTrustPolicy{AllowedSigners: splitValues(*trustedSigners)}, Progress: progressReporter(*progress)}
		var initializer launcher.InitializationManager
		localManager, err := launcher.NewFilesystemComponentManager(options)
		initializer = localManager
		if releaseIndex != nil {
			networkManager, networkErr := launcher.NewNetworkComponentManager(options, *releaseIndex, *releaseIndexURL, *downloadDir, launcher.ArtifactDownloader{AllowHTTPForLoopback: *allowHTTPForLoopback})
			if networkErr != nil {
				err = networkErr
			} else {
				initializer = networkManager
			}
		}
		if err != nil {
			_ = lock.Release()
			fatal(err)
		}
		if *command == "initialize" {
			result, err := initializer.Initialize(ctx)
			if err == nil {
				writeJSON(result)
			}
			if releaseErr := lock.Release(); err == nil {
				err = releaseErr
			}
			if err != nil {
				fatal(err)
			}
			return
		}
		err = initializer.CompleteInitialization(ctx)
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
		if err != nil {
			fatal(err)
		}
		writeJSON(map[string]any{"initialized": true})
		return
	case "settings":
		store, err := newSettingsStore(root, *settingsPath)
		if err != nil {
			fatal(err)
		}
		result, err := store.Load(ctx)
		if err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	case "settings-save":
		if strings.TrimSpace(*settingsInput) == "" {
			fatal(fmt.Errorf("-settings-input is required"))
		}
		data, err := readSettingsInput(*settingsInput)
		if err != nil {
			fatal(err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		var settings launcher.BehaviorSettings
		if err := decoder.Decode(&settings); err != nil {
			fatal(fmt.Errorf("decode settings input: %w", err))
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				fatal(fmt.Errorf("settings input contains trailing JSON"))
			}
			fatal(fmt.Errorf("settings input trailing content: %w", err))
		}
		store, err := newSettingsStore(root, *settingsPath)
		if err != nil {
			fatal(err)
		}
		lock, err := acquireMutationLock(ctx, root, *lockPath)
		if err != nil {
			fatal(err)
		}
		if err := store.Save(ctx, settings); err != nil {
			_ = lock.Release()
			fatal(err)
		}
		if err := lock.Release(); err != nil {
			fatal(err)
		}
		writeJSON(settings)
		return
	case "core-pool-mode-save":
		lock, err := acquireMutationLock(ctx, root, *lockPath)
		if err != nil {
			fatal(err)
		}
		manager, err := launcher.NewCoreManager(root)
		var result json.RawMessage
		if err == nil {
			result, err = manager.SavePoolMode(ctx, *poolMode, *poolID, *expectedRevision)
		}
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
		if err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	case "core-status", "core-start", "core-stop", "core-call":
		if handled, err := runCoreCommand(ctx, *command, root, *coreMethod, *coreParamsJSON); handled {
			if err != nil {
				fatal(err)
			}
			return
		}
	case "job-pool-list", "job-pool-get", "job-pool-apply", "job-pool-scale", "job-pool-drain", "job-pool-resume", "job-pool-delete", "job-pool-operation", "start-slot-session", "slot-session-operation", "environment-list", "environment-install", "environment-upgrade", "environment-verify", "environment-trust", "environment-enable", "environment-disable", "environment-health", "environment-rollback", "environment-operation":
		handled, err := runControlCommand(ctx, *command, root, *jobPoolInput, *poolID, *desiredSlots, *expectedRevision, *idempotencyKey, *actor, *operationID, *environmentID, *environmentVersion, *packageRef, *item)
		if handled {
			if err != nil {
				fatal(err)
			}
			return
		}
	default:
		source, err := resolveSourceRoot(root, *sourceRoot)
		if err != nil {
			fatal(err)
		}
		if managerCommandNeedsItem(*command) && strings.TrimSpace(*item) == "" {
			fatal(fmt.Errorf("-item is required"))
		}
		lock, err := acquireMutationLock(ctx, root, *lockPath)
		if err != nil {
			fatal(err)
		}
		managerOptions := launcher.ManagerOptions{InstallRoot: root, SourceRoot: source, Manifest: manifest, Trust: launcher.PluginTrustPolicy{AllowedSigners: splitValues(*trustedSigners)}, Progress: progressReporter(*progress)}
		if handled, err := runComponentCommand(ctx, *command, *item, managerOptions, releaseIndex, *releaseIndexURL, *downloadDir, *allowHTTPForLoopback, *allowRequiredRemoval); handled {
			if err != nil {
				_ = lock.Release()
				fatal(err)
			}
			if err := lock.Release(); err != nil {
				fatal(err)
			}
			return
		}
		if handled, err := runPluginCommand(ctx, *command, *item, managerOptions, releaseIndex, *releaseIndexURL, *downloadDir, *allowHTTPForLoopback); handled {
			if err != nil {
				_ = lock.Release()
				fatal(err)
			}
			if err := lock.Release(); err != nil {
				fatal(err)
			}
			return
		}
		_ = lock.Release()
		fatal(fmt.Errorf("unknown launcher command %q", *command))
	}
	if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
		fatal(err)
	}
}

func runCoreCommand(ctx context.Context, command, root, method, paramsJSON string) (bool, error) {
	manager, err := launcher.NewCoreManager(root)
	if err != nil {
		return true, err
	}
	switch command {
	case "core-status":
		status, err := manager.Status(ctx)
		if err == nil {
			writeJSON(status)
		}
		return true, err
	case "core-start", "core-stop":
		lock, err := acquireMutationLock(ctx, root, "")
		if err != nil {
			return true, err
		}
		defer lock.Release()
		if command == "core-start" {
			status, err := manager.Start(ctx)
			if err == nil {
				writeJSON(status)
			}
			return true, err
		}
		if err := manager.Stop(ctx); err != nil {
			return true, err
		}
		status, err := manager.Status(ctx)
		if err == nil {
			writeJSON(status)
		}
		return true, err
	case "core-call":
		if strings.TrimSpace(method) == "" {
			return true, fmt.Errorf("-core-method is required")
		}
		if !json.Valid([]byte(paramsJSON)) {
			return true, fmt.Errorf("-core-params-json must be valid JSON")
		}
		result, err := manager.Call(ctx, method, json.RawMessage(paramsJSON))
		if err == nil {
			writeJSON(json.RawMessage(result))
		}
		return true, err
	default:
		return false, nil
	}
}

func runControlCommand(ctx context.Context, command, root, jobPoolInput, poolID string, desiredSlots int, expectedRevision uint64, idempotencyKey, actor, operationID, environmentID, environmentVersion, packageRef string, slotSelector ...string) (bool, error) {
	manager, err := launcher.NewCoreManager(root)
	if err != nil {
		return true, err
	}
	method := ""
	var params any = struct{}{}
	slotID := ""
	if len(slotSelector) > 0 {
		slotID = slotSelector[0]
	}
	switch command {
	case "job-pool-list":
		method = "list_job_pools"
	case "job-pool-get":
		method, params = "get_job_pool", struct {
			PoolID string `json:"pool_id"`
		}{poolID}
	case "job-pool-apply":
		if strings.TrimSpace(jobPoolInput) == "" {
			return true, fmt.Errorf("-job-pool-input is required")
		}
		data, readErr := os.ReadFile(jobPoolInput)
		if readErr != nil {
			return true, readErr
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		var input coreapi.JobPoolApplyRequest
		if err := decoder.Decode(&input); err != nil {
			return true, fmt.Errorf("decode job pool input: %w", err)
		}
		if input.IdempotencyKey == "" {
			input.IdempotencyKey = idempotencyKey
		}
		if input.Actor == "" {
			input.Actor = actor
		}
		if input.ExpectedRevision == 0 {
			input.ExpectedRevision = expectedRevision
		}
		params, method = input, "apply_job_pool"
	case "job-pool-scale":
		method, params = "scale_job_pool", coreapi.JobPoolScaleRequest{PoolID: poolID, DesiredSlots: desiredSlots, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}
	case "job-pool-drain":
		method, params = "drain_job_pool", coreapi.JobPoolActionRequest{PoolID: poolID, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}
	case "job-pool-resume":
		method, params = "resume_job_pool", coreapi.JobPoolActionRequest{PoolID: poolID, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}
	case "job-pool-delete":
		method, params = "delete_job_pool", coreapi.JobPoolDeleteRequest{PoolID: poolID, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}
	case "job-pool-operation":
		method, params = "get_job_pool_operation", struct {
			OperationID string `json:"operation_id"`
		}{operationID}
	case "start-slot-session":
		if (poolID == "") == (slotID == "") {
			return true, fmt.Errorf("-pool-id or -item slot id is required")
		}
		params, method = coreapi.StartSlotSessionRequest{PoolID: poolID, SlotID: slotID, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}, "start_slot_session"
	case "slot-session-operation":
		method, params = "get_slot_session_operation", struct {
			OperationID string `json:"operation_id"`
		}{operationID}
	case "environment-list":
		method = "list_environments"
	case "environment-install", "environment-upgrade", "environment-verify", "environment-trust", "environment-enable", "environment-disable", "environment-health", "environment-rollback":
		if strings.ContainsAny(packageRef, "/\\\\") {
			return true, fmt.Errorf("-package-ref must be a controlled reference")
		}
		op := strings.TrimPrefix(command, "environment-")
		params, method = coreapi.EnvironmentOperationRequest{EnvironmentID: environmentID, Version: environmentVersion, Operation: op, PackageRef: packageRef, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}, "environment_operation"
	case "environment-operation":
		method, params = "get_environment_operation", struct {
			OperationID string `json:"operation_id"`
		}{operationID}
	default:
		return false, nil
	}
	result, err := manager.Call(ctx, method, mustJSON(params))
	if err != nil {
		return true, err
	}
	writeJSON(json.RawMessage(result))
	return true, nil
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func runtimeTarget() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows-amd64"
	case "linux/amd64":
		return "linux-amd64"
	case "linux/arm64":
		return "linux-arm64"
	case "darwin/arm64":
		return "darwin-arm64"
	default:
		return ""
	}
}

func newSettingsStore(root, configured string) (launcher.FileSettingsStore, error) {
	path := configured
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(root, ".chuzi", "launcher-settings.json")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return launcher.FileSettingsStore{}, err
	}
	return launcher.NewFileSettingsStore(path, launcher.DefaultBehaviorSettings())
}

func acquireMutationLock(ctx context.Context, root, configured string) (*launcher.FileLock, error) {
	path := configured
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(root, ".chuzi", "launcher.lock")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return launcher.AcquireFileLock(ctx, filepath.Clean(path))
}

func managerCommandNeedsItem(command string) bool {
	switch command {
	case "component-install", "component-remove", "component-enable", "component-disable",
		"plugin-install", "plugin-update", "plugin-remove", "plugin-enable", "plugin-disable", "plugin-trust", "plugin-untrust":
		return true
	default:
		return false
	}
}

// Core and control-plane commands use the service-owned Core boundary. They
// must remain usable from an installed launcher component even when the
// release manifest is stored with a different component or has not yet been
// materialized in the selected root.
func launcherCommandNeedsManifest(command string, verify bool) bool {
	if verify {
		return true
	}
	switch command {
	case "core-status", "core-start", "core-stop", "core-call", "core-pool-mode-save",
		"job-pool-list", "job-pool-get", "job-pool-apply", "job-pool-scale", "job-pool-drain", "job-pool-resume", "job-pool-delete", "job-pool-operation", "start-slot-session", "slot-session-operation",
		"environment-list", "environment-install", "environment-upgrade", "environment-verify", "environment-trust", "environment-enable", "environment-disable", "environment-health", "environment-rollback", "environment-operation":
		return false
	default:
		return true
	}
}

type stderrProgress struct{ writer io.Writer }

func (p stderrProgress) Report(event launcher.ProgressEvent) {
	if p.writer == nil {
		return
	}
	fmt.Fprintf(p.writer, "progress operation=%s stage=%s item=%q completed=%d total=%d\n", event.Operation, event.Stage, event.Item, event.Completed, event.Total)
}

func progressReporter(enabled bool) launcher.ProgressReporter {
	if !enabled {
		return nil
	}
	return stderrProgress{writer: os.Stderr}
}

func resolveSourceRoot(root, source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return root, nil
	}
	return filepath.Abs(source)
}

func splitValues(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func runComponentCommand(ctx context.Context, command, id string, options launcher.ManagerOptions, index *launcher.ReleaseIndex, indexURL, downloadDir string, allowHTTPForLoopback, allowRequiredRemoval bool) (bool, error) {
	if command == "component-remove" {
		if allowRequiredRemoval && id != "service" {
			return true, fmt.Errorf("%w: required removal is limited to the Core service", launcher.ErrInvalidPath)
		}
		options.AllowRequiredRemoval = allowRequiredRemoval
	}
	var manager launcher.ComponentManager
	local, err := launcher.NewFilesystemComponentManager(options)
	if err != nil {
		return true, err
	}
	manager = local
	if index != nil {
		network, networkErr := launcher.NewNetworkComponentManager(options, *index, indexURL, downloadDir, launcher.ArtifactDownloader{AllowHTTPForLoopback: allowHTTPForLoopback})
		if networkErr != nil {
			return true, networkErr
		}
		manager = network
	}
	switch command {
	case "component-list":
		result, err := manager.List(ctx)
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "component-install":
		result, err := manager.Install(ctx, requiredItem(id))
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "component-remove":
		err := manager.Remove(ctx, requiredItem(id))
		return true, err
	case "component-enable", "component-disable":
		result, err := manager.SetEnabled(ctx, requiredItem(id), command == "component-enable")
		if err == nil {
			writeJSON(result)
		}
		return true, err
	default:
		return false, nil
	}
}

func runPluginCommand(ctx context.Context, command, id string, options launcher.ManagerOptions, index *launcher.ReleaseIndex, indexURL, downloadDir string, allowHTTPForLoopback bool) (bool, error) {
	local, err := launcher.NewFilesystemPluginManager(options)
	if err != nil {
		return true, err
	}
	var manager launcher.PluginManager = local
	if index != nil {
		network, networkErr := launcher.NewNetworkPluginManager(options, *index, indexURL, downloadDir, launcher.ArtifactDownloader{AllowHTTPForLoopback: allowHTTPForLoopback})
		if networkErr != nil {
			return true, networkErr
		}
		manager = network
	}
	switch command {
	case "plugin-list":
		result, err := manager.List(ctx)
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "plugin-install":
		result, err := manager.Install(ctx, requiredItem(id))
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "plugin-update":
		result, err := manager.Update(ctx, requiredItem(id))
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "plugin-remove":
		return true, manager.Remove(ctx, requiredItem(id))
	case "plugin-enable", "plugin-disable":
		result, err := manager.SetEnabled(ctx, requiredItem(id), command == "plugin-enable")
		if err == nil {
			writeJSON(result)
		}
		return true, err
	case "plugin-trust", "plugin-untrust":
		result, err := manager.SetTrusted(ctx, requiredItem(id), command == "plugin-trust")
		if err == nil {
			writeJSON(result)
		}
		return true, err
	default:
		return false, nil
	}
}

func requiredItem(value string) string {
	if strings.TrimSpace(value) == "" {
		fatal(fmt.Errorf("-item is required"))
	}
	return value
}

func writeJSON(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fatal(err)
	}
}

func readSettingsInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
