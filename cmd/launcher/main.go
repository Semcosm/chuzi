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

	"github.com/Semcosm/chuzi/internal/launcher"
)

var version = "dev"

func main() {
	manifestPath := flag.String("manifest", "release-manifest.json", "release manifest path")
	installRoot := flag.String("root", ".", "installation root to inspect")
	verify := flag.Bool("verify", false, "verify declared resources under root")
	command := flag.String("command", "show", "launcher command: show, verify, check-update, initialize, initialize-complete, repair, settings, settings-save, component-list, component-install, component-remove, component-enable, component-disable, plugin-list, plugin-install, plugin-remove, plugin-enable, plugin-disable, plugin-trust, plugin-untrust")
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
	} else if releaseIndex == nil {
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
		if handled, err := runPluginCommand(ctx, *command, *item, managerOptions); handled {
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
		"plugin-install", "plugin-remove", "plugin-enable", "plugin-disable", "plugin-trust", "plugin-untrust":
		return true
	default:
		return false
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

func runPluginCommand(ctx context.Context, command, id string, options launcher.ManagerOptions) (bool, error) {
	manager, err := launcher.NewFilesystemPluginManager(options)
	if err != nil {
		return true, err
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
