package plugin

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInvocationUsesNativeExecutableWithoutShell(t *testing.T) {
	command := Command{Mode: Native, Executable: "bettergi.exe", Args: []string{"--stdio", "value with spaces"}}
	name, args, err := command.Invocation()
	if err != nil {
		t.Fatal(err)
	}
	if name != "bettergi.exe" || !reflect.DeepEqual(args, []string{"--stdio", "value with spaces"}) {
		t.Fatalf("invocation = %q %#v", name, args)
	}
}

func TestInvocationWrapsExecutableWithWineAndRequiresServiceDerivedPrefix(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "wine", "bettergi")
	command := Command{Mode: Wine, WineExecutable: "wine", WinePrefix: prefix, Executable: "C:/BetterGI/BetterGI.exe", Args: []string{"--stdio"}}
	name, args, err := command.Invocation()
	if err != nil {
		t.Fatal(err)
	}
	if name != "wine" || !reflect.DeepEqual(args, []string{"C:/BetterGI/BetterGI.exe", "--stdio"}) {
		t.Fatalf("invocation = %q %#v", name, args)
	}
	invalid := command
	invalid.WinePrefix = "relative-prefix"
	if !errors.Is(invalid.Validate(), ErrInvalidConfig) {
		t.Fatalf("relative Wine prefix accepted: %v", invalid.Validate())
	}
}

func TestLaunchModeCannotMixNativeAndWineSettings(t *testing.T) {
	if err := (Command{Mode: Native, Executable: "plugin", WineExecutable: "wine"}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed launch settings error = %v", err)
	}
	if err := (Command{Mode: Wine, Executable: "plugin", WineExecutable: "wine"}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing Wine prefix error = %v", err)
	}
	if err := (Command{Mode: Native, Executable: "plugin", Environment: []string{"not-an-env"}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid environment error = %v", err)
	}
	if _, err := Start(context.Background(), Command{Mode: Native, Executable: ""}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty executable error = %v", err)
	}
	if err := (Command{Mode: Wine, Executable: "plugin.exe", WineExecutable: "wine", WinePrefix: "/var/lib/chuzi/wine/plugin", Environment: []string{"WINEPREFIX=/tmp/override"}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Wine prefix override was accepted: %v", err)
	}
}
