package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/matrix"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

// TestControlledMatrixHomeserverSyncSend is intentionally opt-in. It is the
// production-like check for a real Matrix test deployment; normal CI keeps
// using TestProductionMatrixSyncSendAndOutboxRecoveryAcrossRestart and never
// receives a live access token.
//
// Required environment:
//
//	CHUZI_MATRIX_TEST_HOMESERVER  homeserver origin
//	CHUZI_MATRIX_TEST_BOT_TOKEN   bot access token
//	CHUZI_MATRIX_TEST_BOT_USER    bot MXID
//	CHUZI_MATRIX_TEST_ACTOR_TOKEN separate test-user token
//	CHUZI_MATRIX_TEST_ACTOR_USER  separate test-user MXID
//	CHUZI_MATRIX_TEST_ROOM        joined test room ID
//	CHUZI_MATRIX_TEST_ACCOUNT     fake account ID accepted by the test adapter
//
// The actor token is used only to inject one command event. The bot token is
// then verified through the same sync/send client used by the service. The
// room must be disposable: the test filters by its unique event ID and does
// not delete or modify unrelated room history.
func TestControlledMatrixHomeserverSyncSend(t *testing.T) {
	values := map[string]string{}
	for _, name := range []string{
		"CHUZI_MATRIX_TEST_HOMESERVER", "CHUZI_MATRIX_TEST_BOT_TOKEN", "CHUZI_MATRIX_TEST_BOT_USER",
		"CHUZI_MATRIX_TEST_ACTOR_TOKEN", "CHUZI_MATRIX_TEST_ACTOR_USER", "CHUZI_MATRIX_TEST_ROOM", "CHUZI_MATRIX_TEST_ACCOUNT",
	} {
		values[name] = strings.TrimSpace(os.Getenv(name))
		if values[name] == "" {
			t.Skip("controlled Matrix homeserver variables are not configured")
		}
	}
	client, err := matrix.NewHTTPClient(matrix.HTTPClientConfig{
		HomeserverURL: values["CHUZI_MATRIX_TEST_HOMESERVER"],
		AccessToken:   values["CHUZI_MATRIX_TEST_BOT_TOKEN"],
		HTTPClient:    &http.Client{Timeout: 90 * time.Second},
		UserAgent:     "chuzi-controlled-runtime-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("bot whoami failed: %v", err)
	}
	cfg, err := config.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateAccount(values["CHUZI_MATRIX_TEST_ACCOUNT"]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sequence := 0
	requests, err := requestservice.New(database, func() time.Time { return now }, func(kind string) string {
		sequence++
		return fmt.Sprintf("live-%s-%d", kind, sequence)
	}, "live-matrix-test")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := matrix.NewAdapter(requests, matrix.Policy{Rooms: map[string]map[string]matrix.Role{
		values["CHUZI_MATRIX_TEST_ROOM"]: {values["CHUZI_MATRIX_TEST_ACTOR_USER"]: matrix.RoleUser},
	}}, matrix.Config{UserID: values["CHUZI_MATRIX_TEST_BOT_USER"], Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := matrix.NewGateway(matrix.GatewayConfig{Client: client, Adapter: adapter, SyncTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	eventID := fmt.Sprintf("chuzi-runtime-test-%d", time.Now().UnixNano())
	body := fmt.Sprintf("!ugs request %s", values["CHUZI_MATRIX_TEST_ACCOUNT"])
	if err := sendAsActor(context.Background(), values["CHUZI_MATRIX_TEST_HOMESERVER"], values["CHUZI_MATRIX_TEST_ACTOR_TOKEN"], values["CHUZI_MATRIX_TEST_ROOM"], eventID, body); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- gateway.Run(ctx) }()
	for {
		notifications, listErr := database.ListNotifications()
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(notifications) > 0 {
			cancel()
			if err := <-runErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("live Matrix gateway error = %v", err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, openErr := store.Open(cfg)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer restarted.Close()
			notifier, notifierErr := matrix.NewNotifier(restarted, client, matrix.NotifierConfig{
				Owner: "live-matrix-notifier", ClaimTTL: time.Minute, RetryBase: time.Second, RetryMax: time.Second,
				BatchSize: 10, Clock: func() time.Time { return now },
			})
			if notifierErr != nil {
				t.Fatal(notifierErr)
			}
			result, flushErr := notifier.Flush(context.Background())
			if flushErr != nil || result.Delivered != 1 {
				t.Fatalf("live Matrix outbox flush = %#v, %v", result, flushErr)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for live Matrix sync event: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func sendAsActor(ctx context.Context, homeserver, token, roomID, eventID, body string) error {
	base, err := url.Parse(strings.TrimSpace(homeserver))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("invalid controlled Matrix homeserver")
	}
	payload, err := json.Marshal(map[string]string{"msgtype": "m.text", "body": body})
	if err != nil {
		return err
	}
	target := *base
	if strings.ContainsRune(roomID, '/') || strings.ContainsRune(eventID, '/') {
		return errors.New("invalid controlled Matrix path segment")
	}
	// Keep IDs unescaped while assigning URL.Path so url.URL.String escapes
	// each segment once. Escaping first would send %2521... to Synapse.
	target.Path = strings.TrimRight(target.Path, "/") + "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/" + eventID
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return fmt.Errorf("actor Matrix send returned HTTP %d", response.StatusCode)
	}
	return nil
}
