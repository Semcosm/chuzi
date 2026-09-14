package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/automation"
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

func TestProcessWaitLeavesProtocolReaderAtEOF(t *testing.T) {
	process, err := start(context.Background(), Command{
		Mode:        Native,
		Executable:  os.Args[0],
		Args:        []string{"-test.run=TestPluginHelperProcess", "--"},
		Environment: []string{"CHUZI_PLUGIN_HELPER=1"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = process.stdin.Close()
		_ = process.stdout.Close()
	}()

	request := automation.Request("hello", automation.Hello, nil)
	if err := json.NewEncoder(process.stdin).Encode(request); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bufio.NewReader(process.stdout))
	var response automation.Envelope
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("handshake response: %v", err)
	}
	if response.Type != automation.HelloAck {
		t.Fatalf("handshake response type = %q", response.Type)
	}
	if err := process.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := process.Wait(waitCtx); err == nil {
		t.Fatal("Wait() unexpectedly reported a clean exit after cancellation")
	}
	var trailing automation.Envelope
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("protocol reader after Wait() = %v, want EOF", err)
	}
}
