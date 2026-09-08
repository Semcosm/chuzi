package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/Semcosm/chuzi/internal/protocol"
)

var version = "dev"

type workerSession struct {
	command string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	decoder *json.Decoder
	wait    <-chan error
}

func startWorker(ctx context.Context, command, script string) (*workerSession, error) {
	cmd := exec.CommandContext(ctx, command, script, "--stdio")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create worker stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create worker stdin pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker %q: %w", command, err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	return &workerSession{
		command: command,
		cmd:     cmd,
		stdin:   stdin,
		decoder: json.NewDecoder(bufio.NewReader(stdout)),
		wait:    wait,
	}, nil
}

func (w *workerSession) request(request protocol.Envelope) (protocol.Envelope, error) {
	if err := json.NewEncoder(w.stdin).Encode(request); err != nil {
		return protocol.Envelope{}, fmt.Errorf("send %s to %s: %w", request.Type, w.command, err)
	}

	var response protocol.Envelope
	if err := w.decoder.Decode(&response); err != nil {
		return protocol.Envelope{}, fmt.Errorf("read response for %s: %w", request.Type, err)
	}
	if response.Protocol != protocol.Version {
		return protocol.Envelope{}, fmt.Errorf("worker protocol %q does not match %q", response.Protocol, protocol.Version)
	}
	if response.ID != request.ID {
		return protocol.Envelope{}, fmt.Errorf("worker response id %q does not match %q", response.ID, request.ID)
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (w *workerSession) stop() {
	if w == nil || w.stdin == nil {
		return
	}
	_ = w.stdin.Close()
	select {
	case <-w.wait:
	case <-time.After(2 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.wait
	}
}

func runSelfTest(ctx context.Context, command, script string) error {
	worker, err := startWorker(ctx, command, script)
	if err != nil {
		return err
	}
	defer worker.stop()

	response, err := worker.request(protocol.Request("hello-1", "hello", map[string]string{
		"service": "chuzi",
		"version": version,
	}))
	if err != nil {
		return err
	}
	if response.Type != "hello_ack" {
		return fmt.Errorf("unexpected hello response type %q", response.Type)
	}
	if _, err := worker.request(protocol.Request("shutdown-1", "shutdown", nil)); err != nil {
		return err
	}
	return nil
}

func run(ctx context.Context, command, script string) error {
	worker, err := startWorker(ctx, command, script)
	if err != nil {
		return err
	}
	defer worker.stop()

	if _, err := worker.request(protocol.Request("hello-1", "hello", map[string]string{
		"service": "chuzi",
		"version": version,
	})); err != nil {
		return err
	}
	log.Printf("chuzi service %s is ready; worker=%s", version, command)

	select {
	case <-ctx.Done():
		return nil
	case err := <-worker.wait:
		if err != nil {
			return fmt.Errorf("worker exited: %w", err)
		}
		return errors.New("worker exited unexpectedly")
	}
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
