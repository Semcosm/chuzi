// chuzi-launcher is the small, UI-neutral bootstrap command shipped with a
// nightly package. A future desktop UI can call the same manifest and
// verification contract without owning release or filesystem policy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Semcosm/chuzi/internal/launcher"
)

var version = "dev"

func main() {
	manifestPath := flag.String("manifest", "release-manifest.json", "release manifest path")
	installRoot := flag.String("root", ".", "installation root to inspect")
	verify := flag.Bool("verify", false, "verify declared resources under root")
	command := flag.String("command", "show", "launcher command: show, verify, check-update, repair, component-list, component-install, component-remove, component-enable, component-disable, plugin-list, plugin-install, plugin-remove, plugin-enable, plugin-disable, plugin-trust, plugin-untrust")
	sourceRoot := flag.String("source-root", "", "trusted local source root for repair/install")
	updateManifest := flag.String("update-manifest", "", "candidate manifest for check-update")
	currentVersion := flag.String("current-version", "", "installed version for check-update")
	item := flag.String("item", "", "component or plugin id")
	paths := flag.String("paths", "", "comma-separated resource paths for repair (default: all)")
	trustedSigners := flag.String("trusted-signers", "", "comma-separated plugin signer allowlist")
	showVersion := flag.Bool("version", false, "print launcher version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	data, err := os.ReadFile(*manifestPath)
	if err != nil {
		fatal(err)
	}
	var manifest launcher.ReleaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		fatal(err)
	}
	if *verify || *command == "verify" {
		root, err := filepath.Abs(*installRoot)
		if err != nil {
			fatal(err)
		}
		result, err := (launcher.FileVerifier{}).Verify(context.Background(), root, manifest)
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
		if strings.TrimSpace(*updateManifest) == "" || strings.TrimSpace(*currentVersion) == "" {
			fatal(fmt.Errorf("-update-manifest and -current-version are required"))
		}
		result, err := (launcher.ManifestUpdateChecker{Source: launcher.FileManifestSource{Path: *updateManifest}}).Check(context.Background(), launcher.UpdateRequest{CurrentVersion: *currentVersion, Target: manifest.Target, Channel: manifest.Channel})
		if err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	case "repair":
		source, err := resolveSourceRoot(root, *sourceRoot)
		if err != nil {
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
		result, err := (launcher.FileRepairer{SourceRoot: source}).Repair(context.Background(), request)
		if err != nil {
			fatal(err)
		}
		writeJSON(result)
		return
	default:
		source, err := resolveSourceRoot(root, *sourceRoot)
		if err != nil {
			fatal(err)
		}
		managerOptions := launcher.ManagerOptions{InstallRoot: root, SourceRoot: source, Manifest: manifest, Trust: launcher.PluginTrustPolicy{AllowedSigners: splitValues(*trustedSigners)}}
		if handled, err := runComponentCommand(context.Background(), *command, *item, managerOptions); handled {
			if err != nil {
				fatal(err)
			}
			return
		}
		if handled, err := runPluginCommand(context.Background(), *command, *item, managerOptions); handled {
			if err != nil {
				fatal(err)
			}
			return
		}
		fatal(fmt.Errorf("unknown launcher command %q", *command))
	}
	if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
		fatal(err)
	}
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

func runComponentCommand(ctx context.Context, command, id string, options launcher.ManagerOptions) (bool, error) {
	manager, err := launcher.NewFilesystemComponentManager(options)
	if err != nil {
		return true, err
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
