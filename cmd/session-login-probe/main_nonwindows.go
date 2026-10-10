//go:build !windows

package main

import (
	"fmt"
	"os"
)

func runProbe() {
	fmt.Fprintln(os.Stderr, "native Windows session probe is pending on this host")
	os.Exit(2)
}
