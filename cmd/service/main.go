package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
)

var version = "dev"

func runSelfTest(ctx context.Context, command, script string) error {
	root, err := os.MkdirTemp("", "chuzi-self-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cfg, err := config.New(filepath.Join(root, "runtime"))
	if err != nil {
		return err
	}
	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return err
	}
	profile, err := profiles.Prepare("self-test-account")
	if err != nil {
		return err
	}
	factory, err := browser.NewProcessFactory(browser.ProcessConfig{
		Command:    command,
		Script:     script,
		Stderr:     os.Stderr,
		WorkerMode: "success",
	})
	if err != nil {
		return err
	}
	worker, err := factory.Start(ctx, browser.WorkerSpec{
		SessionID:  "self-test-session",
		AccountID:  "self-test-account",
		RequestID:  "self-test-request",
		ProfileDir: profile,
		LeaseID:    "self-test-lease",
		Owner:      "self-test",
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = worker.Close(closeCtx)
	}()
	result, err := worker.Run(ctx)
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return fmt.Errorf("worker self-test failed: %s", result.Failure)
	}
	return nil
}

func run(ctx context.Context, command, script string) error {
	root, err := os.MkdirTemp("", "chuzi-runtime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cfg, err := config.New(filepath.Join(root, "runtime"))
	if err != nil {
		return err
	}
	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return err
	}
	profile, err := profiles.Prepare("service-placeholder")
	if err != nil {
		return err
	}
	factory, err := browser.NewProcessFactory(browser.ProcessConfig{
		Command:    command,
		Script:     script,
		Stderr:     os.Stderr,
		WorkerMode: "hold",
	})
	if err != nil {
		return err
	}
	worker, err := factory.Start(ctx, browser.WorkerSpec{
		SessionID:  "service-placeholder-session",
		AccountID:  "service-placeholder",
		RequestID:  "service-placeholder-request",
		ProfileDir: profile,
		LeaseID:    "service-placeholder-lease",
		Owner:      "service",
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = worker.Close(closeCtx)
	}()
	log.Printf("chuzi service %s is ready; worker=%s", version, command)
	if _, err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker session ended: %w", err)
	}
	return nil
}

func main() {
	workerCommand := flag.String("worker-command", "node", "browser worker executable")
	workerScript := flag.String("worker-script", "browser-worker/src/worker.mjs", "browser worker script")
	selfTest := flag.Bool("self-test", false, "run the worker protocol smoke test and exit")
	showVersion := flag.Bool("version", false, "print the service version")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	if *selfTest {
		err = runSelfTest(ctx, *workerCommand, *workerScript)
	} else {
		err = run(ctx, *workerCommand, *workerScript)
	}
	if err != nil {
		log.Fatal(err)
	}
}
