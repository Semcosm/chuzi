package launcher

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestProcessServiceControllerStartStatusStop(t *testing.T) {
	if os.Getenv("CHUZI_LAUNCHER_SERVICE_HELPER") == "1" {
		select {}
	}
	controller := &ProcessServiceController{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestProcessServiceControllerStartStatusStop", "-test.v=false"},
		Env:     []string{"CHUZI_LAUNCHER_SERVICE_HELPER=1"},
	}
	state, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != ServiceStopped {
		t.Fatalf("initial status = %#v, want stopped", state)
	}
	state, err = controller.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != ServiceRunning || state.PID == 0 {
		t.Fatalf("start state = %#v", state)
	}
	if _, err := controller.Start(context.Background()); !errors.Is(err, ErrServiceRunning) {
		t.Fatalf("second start = %v, want ErrServiceRunning", err)
	}
	running, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != ServiceRunning || running.PID != state.PID {
		t.Fatalf("running status = %#v, want pid %d", running, state.PID)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	stopped, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != ServiceStopped || !stopped.HasExit {
		t.Fatalf("stopped status = %#v", stopped)
	}
}

func TestProcessServiceControllerRejectsInvalidEnvironment(t *testing.T) {
	controller := &ProcessServiceController{Command: "service", Env: []string{"invalid"}}
	if _, err := controller.Start(context.Background()); err == nil {
		t.Fatal("invalid environment entry was accepted")
	}
}
