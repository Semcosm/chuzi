package servicehost

import "context"

type ServiceState string

const (
	StateStopped      ServiceState = "stopped"
	StateRunning      ServiceState = "running"
	StateStartPending ServiceState = "start_pending"
	StateStopPending  ServiceState = "stop_pending"
)

type Status struct {
	State ServiceState
	PID   int
}

type Controller interface {
	Start(context.Context) (Status, error)
	Stop(context.Context) error
	Status(context.Context) (Status, error)
}

// Installer is intentionally separate from Controller: only the fixed
// management entrypoint may mutate SCM registration.
type Installer interface {
	Install(context.Context) error
	Uninstall(context.Context) error
}
