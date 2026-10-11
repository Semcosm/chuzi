package main

import (
	"fmt"
	"os"
)

func main() {
	if err := runSessionBroker(); err != nil {
		fmt.Fprintln(os.Stderr, "chuzi session broker: broker_unavailable")
		os.Exit(1)
	}
}
