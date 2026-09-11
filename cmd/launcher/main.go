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

	"github.com/Semcosm/chuzi/internal/launcher"
)

var version = "dev"

func main() {
	manifestPath := flag.String("manifest", "release-manifest.json", "release manifest path")
	installRoot := flag.String("root", ".", "installation root to inspect")
	verify := flag.Bool("verify", false, "verify declared resources under root")
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
	if *verify {
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
	if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
