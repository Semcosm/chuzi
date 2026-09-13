// Package matrix contains the transport-neutral Matrix command and delivery
// boundaries. It does not open a Matrix connection or handle credentials.
package matrix

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/observability"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

const DefaultPrefix = "!ugs"

var (
	ErrInvalidEvent   = errors.New("matrix: invalid event")
	ErrInvalidCommand = errors.New("matrix: invalid command")
	ErrNotAuthorized  = errors.New("matrix: user or room is not authorized")
	ErrNotVisible     = errors.New("matrix: request is not visible in this room")
)

// CommandKind is the small command set exposed by the initial adapter.
type CommandKind string

const (
	CommandStatus  CommandKind = "status"
	CommandRequest CommandKind = "request"
	CommandCancel  CommandKind = "cancel"
	CommandHelp    CommandKind = "help"
)

// Command is the parsed, transport-neutral form of one Matrix command.
type Command struct {
	Kind  CommandKind
	Value string
}

// IncomingEvent is the only Matrix transport data the command layer needs.
// EventID is used to derive stable request and reply IDs for retries.
type IncomingEvent struct {
	EventID string
	RoomID  string
	UserID  string
	Body    string
}

// Reply is a safe response for the transport adapter to send. It contains no
// account credentials, internal errors, or unredacted account identifiers.
type Reply struct {
	EventID   string
	RoomID    string
	RequestID string
	Body      string
}

// Role controls whether a caller may inspect requests outside the originating
// room. Regular users are room-scoped; administrators are explicitly allowed
// to inspect another room's request ID.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Policy is an explicit room/user allowlist. A missing room or user is denied.
type Policy struct {
	Rooms map[string]map[string]Role
}

// Authorize checks one room/user pair and returns its configured role.
func (p Policy) Authorize(roomID, userID string) (Role, error) {
	if !safeInputToken(roomID) || !safeInputToken(userID) {
		return "", ErrNotAuthorized
	}
	users, ok := p.Rooms[roomID]
	if !ok {
		return "", ErrNotAuthorized
	}
	role, ok := users[userID]
	if !ok || (role != RoleUser && role != RoleAdmin) {
		return "", ErrNotAuthorized
	}
	return role, nil
}

func (p Policy) clone() Policy {
	copyPolicy := Policy{Rooms: make(map[string]map[string]Role, len(p.Rooms))}
	for roomID, users := range p.Rooms {
		copyPolicy.Rooms[roomID] = make(map[string]Role, len(users))
		for userID, role := range users {
			copyPolicy.Rooms[roomID][userID] = role
		}
	}
	return copyPolicy
}

// Config controls parsing and redacted event recording.
type Config struct {
	Prefix string
	UserID string
	Clock  func() time.Time
	Sink   observability.Sink
}

// Adapter translates authorized commands into Request Service operations.
// It never accesses the credential store or mutates account state directly.
type Adapter struct {
	requests *requestservice.Service
	policy   Policy
	config   Config
}

// NewAdapter validates and constructs a Matrix command adapter.
func NewAdapter(requests *requestservice.Service, policy Policy, config Config) (*Adapter, error) {
	if requests == nil {
		return nil, ErrInvalidEvent
	}
	if strings.TrimSpace(config.Prefix) == "" {
		config.Prefix = DefaultPrefix
	}
	if !safeInputToken(config.Prefix) {
		return nil, ErrInvalidCommand
	}
	if config.UserID != "" && !safeInputToken(config.UserID) {
		return nil, ErrInvalidCommand
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
	}
	return &Adapter{requests: requests, policy: policy.clone(), config: config}, nil
}

func (a *Adapter) configUserID() string {
	if a == nil {
		return ""
	}
	return a.config.UserID
}

// Handle authorizes, parses and executes one incoming Matrix command.
func (a *Adapter) Handle(event IncomingEvent) (Reply, error) {
	if a == nil || !validIncomingEvent(event) {
		return Reply{}, ErrInvalidEvent
	}
	role, err := a.policy.Authorize(event.RoomID, event.UserID)
	if err != nil {
		a.record(event, "authorize", "denied", "", "not_authorized")
		return Reply{}, err
	}
	command, err := ParseCommandWithPrefix(event.Body, a.config.Prefix)
	if err != nil {
		a.record(event, "parse", "denied", "", "invalid_command")
		return Reply{}, err
	}
	switch command.Kind {
	case CommandHelp:
		body := fmt.Sprintf("commands: %s status <request-id> | %s request <account-id> | %s cancel <request-id> | %s help", a.config.Prefix, a.config.Prefix, a.config.Prefix, a.config.Prefix)
		reply := a.reply(event, command.Kind, body, "")
		a.record(event, "help", "accepted", "", "")
		return reply, nil
	case CommandRequest:
		requestID := derivedID("request", event.EventID)
		request, idempotent, err := a.requests.Submit(requestservice.SubmitInput{
			RequestID:          requestID,
			AccountID:          command.Value,
			IdempotencyKey:     derivedID("matrix-event", event.EventID),
			NotificationRoomID: event.RoomID,
			Actor:              event.UserID,
		})
		if err != nil {
			a.record(event, "request", "failed", requestID, classifyError(err))
			return Reply{}, err
		}
		body := fmt.Sprintf("request=%s account=%s status=%s", request.RequestID, observability.RedactIdentifier(request.AccountID), request.State)
		if idempotent {
			body += " duplicate=true"
		}
		reply := a.reply(event, command.Kind, body, request.RequestID)
		a.record(event, "request", "accepted", request.RequestID, "")
		return reply, nil
	case CommandStatus:
		request, err := a.requests.Status(command.Value)
		if err != nil {
			a.record(event, "status", "failed", command.Value, classifyError(err))
			return Reply{}, err
		}
		if !visible(role, event.RoomID, request) {
			a.record(event, "status", "denied", request.RequestID, "not_visible")
			return Reply{}, ErrNotVisible
		}
		body := fmt.Sprintf("request=%s account=%s status=%s", request.RequestID, observability.RedactIdentifier(request.AccountID), request.State)
		if request.LastFailure != "" {
			body += " failure=" + string(request.LastFailure)
		}
		reply := a.reply(event, command.Kind, body, request.RequestID)
		a.record(event, "status", "accepted", request.RequestID, "")
		return reply, nil
	case CommandCancel:
		request, err := a.requests.Status(command.Value)
		if err != nil {
			a.record(event, "cancel", "failed", command.Value, classifyError(err))
			return Reply{}, err
		}
		if !visible(role, event.RoomID, request) {
			a.record(event, "cancel", "denied", request.RequestID, "not_visible")
			return Reply{}, ErrNotVisible
		}
		cancelled, err := a.requests.Cancel(request.RequestID, event.UserID, "matrix cancel")
		if err != nil {
			a.record(event, "cancel", "failed", request.RequestID, classifyError(err))
			return Reply{}, err
		}
		body := fmt.Sprintf("request=%s account=%s status=%s", cancelled.RequestID, observability.RedactIdentifier(cancelled.AccountID), cancelled.State)
		reply := a.reply(event, command.Kind, body, cancelled.RequestID)
		a.record(event, "cancel", "accepted", cancelled.RequestID, "")
		return reply, nil
	default:
		return Reply{}, ErrInvalidCommand
	}
}

// ParseCommand parses a command with the default !ugs prefix.
func ParseCommand(body string) (Command, error) {
	return ParseCommandWithPrefix(body, DefaultPrefix)
}

// ParseCommandWithPrefix enforces the exact initial command grammar and
// rejects trailing arguments so untrusted text cannot become a hidden option.
func ParseCommandWithPrefix(body, prefix string) (Command, error) {
	if !safeInputToken(prefix) || len(body) > 1024 {
		return Command{}, ErrInvalidCommand
	}
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 || fields[0] != prefix {
		return Command{}, ErrInvalidCommand
	}
	switch {
	case len(fields) == 2 && fields[1] == string(CommandHelp):
		return Command{Kind: CommandHelp}, nil
	case len(fields) == 3 && fields[1] == string(CommandStatus):
		if !safeInputToken(fields[2]) {
			return Command{}, ErrInvalidCommand
		}
		return Command{Kind: CommandStatus, Value: fields[2]}, nil
	case len(fields) == 3 && fields[1] == string(CommandRequest):
		if !safeInputToken(fields[2]) {
			return Command{}, ErrInvalidCommand
		}
		return Command{Kind: CommandRequest, Value: fields[2]}, nil
	case len(fields) == 3 && fields[1] == string(CommandCancel):
		if !safeInputToken(fields[2]) {
			return Command{}, ErrInvalidCommand
		}
		return Command{Kind: CommandCancel, Value: fields[2]}, nil
	default:
		return Command{}, ErrInvalidCommand
	}
}

func visible(role Role, roomID string, request store.Request) bool {
	return role == RoleAdmin || (request.NotificationRoomID != "" && request.NotificationRoomID == roomID)
}

func (a *Adapter) reply(event IncomingEvent, kind CommandKind, body, requestID string) Reply {
	return Reply{EventID: derivedID("reply-"+string(kind), event.EventID), RoomID: event.RoomID, RequestID: requestID, Body: body}
}

func (a *Adapter) record(event IncomingEvent, operation, outcome, requestID, errorClass string) {
	a.config.Sink.Record(observability.Event{
		At:         a.config.Clock(),
		Component:  "matrix",
		Operation:  operation,
		Outcome:    outcome,
		RequestID:  requestID,
		Resource:   observability.RedactIdentifier(event.RoomID),
		ErrorClass: errorClass,
	})
}

func validIncomingEvent(event IncomingEvent) bool {
	return safeInputToken(event.EventID) && safeInputToken(event.RoomID) &&
		safeInputToken(event.UserID) && len(event.Body) <= 1024
}

func safeInputToken(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func derivedID(kind, value string) string {
	digest := sha256.Sum256([]byte(value))
	return kind + "_" + hex.EncodeToString(digest[:])[:20]
}

func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, store.ErrRequestNotFound):
		return "not_found"
	case errors.Is(err, store.ErrAccountBusy):
		return "account_busy"
	case errors.Is(err, ErrNotVisible), errors.Is(err, ErrNotAuthorized):
		return "not_authorized"
	default:
		return "operation_failed"
	}
}
