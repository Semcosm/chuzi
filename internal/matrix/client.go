package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidClient = errors.New("matrix: invalid HTTP client")
	ErrHTTPFailure   = errors.New("matrix: HTTP request failed")
	ErrUnauthorized  = errors.New("matrix: unauthorized")
	ErrSyncProtocol  = errors.New("matrix: invalid sync response")
)

// HTTPClientConfig contains the non-secret Matrix endpoint and an injected
// access token. The token is never included in errors or serialized data.
type HTTPClientConfig struct {
	HomeserverURL string
	AccessToken   string
	UserAgent     string
	HTTPClient    *http.Client
}

// HTTPClient implements the minimal Matrix Client-Server API needed by the
// command and notification boundaries.
type HTTPClient struct {
	base      *url.URL
	token     string
	userAgent string
	http      *http.Client
}

func NewHTTPClient(config HTTPClientConfig) (*HTTPClient, error) {
	if strings.TrimSpace(config.AccessToken) == "" {
		return nil, ErrInvalidClient
	}
	base, err := url.Parse(strings.TrimSpace(config.HomeserverURL))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrInvalidClient
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPClient{base: base, token: config.AccessToken, userAgent: strings.TrimSpace(config.UserAgent), http: config.HTTPClient}, nil
}

func (c *HTTPClient) Send(ctx context.Context, roomID, eventID, body string) error {
	if c == nil || ctx == nil || !safeInputToken(roomID) || !safeInputToken(eventID) || strings.TrimSpace(body) == "" || len(body) > 4096 {
		return ErrInvalidClient
	}
	payload := struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	}{MsgType: "m.text", Body: body}
	data, err := json.Marshal(payload)
	if err != nil {
		return ErrHTTPFailure
	}
	path := "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/send/m.room.message/" + url.PathEscape(eventID)
	return c.doJSON(ctx, http.MethodPut, path, data, nil)
}

// WhoAmI verifies that the configured token is accepted without exposing the
// returned user ID to callers.
func (c *HTTPClient) Health(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrInvalidClient
	}
	return c.doJSON(ctx, http.MethodGet, "/_matrix/client/v3/account/whoami", nil, nil)
}

type SyncEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	Sender  string `json:"sender"`
	Content struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	} `json:"content"`
}

type SyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]struct {
			Timeline struct {
				Events []SyncEvent `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

// Sync performs one long-poll sync request. It returns only message events
// from joined rooms; unknown event fields are ignored by design.
func (c *HTTPClient) Sync(ctx context.Context, since string, timeout time.Duration) (SyncResponse, error) {
	if c == nil || ctx == nil || timeout <= 0 || timeout > 5*time.Minute {
		return SyncResponse{}, ErrInvalidClient
	}
	path := "/_matrix/client/v3/sync?timeout=" + url.QueryEscape(fmt.Sprint(timeout.Milliseconds()))
	if strings.TrimSpace(since) != "" {
		path += "&since=" + url.QueryEscape(since)
	}
	var result SyncResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return SyncResponse{}, err
	}
	if strings.TrimSpace(result.NextBatch) == "" {
		return SyncResponse{}, ErrSyncProtocol
	}
	return result, nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, body []byte, result any) error {
	target := *c.base
	if strings.HasPrefix(path, "/") {
		target.RawQuery = ""
		pathOnly := path
		if index := strings.IndexByte(path, '?'); index >= 0 {
			pathOnly = path[:index]
			target.RawQuery = path[index+1:]
		}
		target.Path = strings.TrimRight(target.Path, "/") + pathOnly
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return ErrHTTPFailure
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrHTTPFailure
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.CopyN(io.Discard, response.Body, 1024)
		return ErrHTTPFailure
	}
	if result == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(result); err != nil {
		return ErrSyncProtocol
	}
	return nil
}

// Gateway connects sync events to the existing transport-neutral adapter.
// It has no business-state logic of its own and stops on context cancellation.
type Gateway struct {
	client       *HTTPClient
	adapter      *Adapter
	syncTimeout  time.Duration
	pollInterval time.Duration
}

type GatewayConfig struct {
	Client       *HTTPClient
	Adapter      *Adapter
	SyncTimeout  time.Duration
	PollInterval time.Duration
}

func NewGateway(config GatewayConfig) (*Gateway, error) {
	if config.Client == nil || config.Adapter == nil || config.SyncTimeout <= 0 || config.SyncTimeout > 5*time.Minute || config.PollInterval < 0 {
		return nil, ErrInvalidClient
	}
	return &Gateway{client: config.Client, adapter: config.Adapter, syncTimeout: config.SyncTimeout, pollInterval: config.PollInterval}, nil
}

func (g *Gateway) Run(ctx context.Context) error {
	if g == nil || ctx == nil {
		return ErrInvalidClient
	}
	since := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sync, err := g.client.Sync(ctx, since, g.syncTimeout)
		if err != nil {
			return err
		}
		for roomID, room := range sync.Rooms.Join {
			for _, event := range room.Timeline.Events {
				if event.Type != "m.room.message" || event.Content.MsgType != "m.text" || event.Sender == "" || event.Sender == g.adapterBotUser() {
					continue
				}
				reply, handleErr := g.adapter.Handle(IncomingEvent{EventID: event.EventID, RoomID: roomID, UserID: event.Sender, Body: event.Content.Body})
				if handleErr != nil {
					if errors.Is(handleErr, ErrNotAuthorized) || errors.Is(handleErr, ErrInvalidCommand) || errors.Is(handleErr, ErrNotVisible) {
						continue
					}
					return handleErr
				}
				if err := g.client.Send(ctx, reply.RoomID, reply.EventID, reply.Body); err != nil {
					return err
				}
			}
		}
		since = sync.NextBatch
		if g.pollInterval > 0 {
			timer := time.NewTimer(g.pollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (g *Gateway) adapterBotUser() string {
	return g.adapter.configUserID()
}
