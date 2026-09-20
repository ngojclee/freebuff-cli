package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUpstream is a scripted stand-in for codebuff.com. It answers only the three calls
// the pool makes, so a test can drive the state machine deterministically.
type fakeUpstream struct {
	sessionStatuses []string
	sessionCalls    atomic.Int64
	startRunStatus  int
	runCounter      atomic.Int64
}

func (f *fakeUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/freebuff/session", func(w http.ResponseWriter, r *http.Request) {
		index := int(f.sessionCalls.Add(1)) - 1
		status := "active"
		if len(f.sessionStatuses) > 0 {
			if index >= len(f.sessionStatuses) {
				index = len(f.sessionStatuses) - 1
			}
			status = f.sessionStatuses[index]
		}
		payload := map[string]any{"status": status, "instanceId": "inst-1"}
		if status == "queued" {
			payload["position"] = 4
			payload["queueDepth"] = 9
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		if f.startRunStatus != 0 && f.startRunStatus != http.StatusOK {
			w.WriteHeader(f.startRunStatus)
			_, _ = w.Write([]byte(`{"error":"upstream_auth_rejected"}`))
			return
		}
		id := f.runCounter.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"runId": "run-" + string(rune('0'+id))})
	})
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	return mux
}

func testPool(t *testing.T, upstream *fakeUpstream, accounts []freebuffAccount, cfg Config) (*tokenPool, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(upstream.handler())
	t.Cleanup(server.Close)
	cfg.UpstreamBaseURL = server.URL
	client, errClient := newFreebuffClient(cfg)
	if errClient != nil {
		t.Fatalf("newFreebuffClient: %v", errClient)
	}
	return newTokenPool(cfg, client, accounts, time.Now), server
}

func account(token, label string) freebuffAccount {
	return freebuffAccount{AuthToken: token, Email: label, Enabled: true}
}

func TestPoolRotatesAcrossAccounts(t *testing.T) {
	upstream := &fakeUpstream{sessionStatuses: []string{"active"}}
	pool, _ := testPool(t, upstream, []freebuffAccount{
		account("token-aaaaaaaaaaaaaaaa", "a@example.com"),
		account("token-bbbbbbbbbbbbbbbb", "b@example.com"),
	}, DefaultConfig())

	ctx := context.Background()
	first, errFirst := pool.Acquire(ctx, "base2-free")
	if errFirst != nil {
		t.Fatalf("first acquire: %v", errFirst)
	}
	second, errSecond := pool.Acquire(ctx, "base2-free")
	if errSecond != nil {
		t.Fatalf("second acquire: %v", errSecond)
	}
	if first.pool.name == second.pool.name {
		t.Fatalf("round robin returned the same account twice: %s", first.pool.name)
	}
}

func TestPoolSkipsACoolingAccount(t *testing.T) {
	upstream := &fakeUpstream{sessionStatuses: []string{"active"}}
	pool, _ := testPool(t, upstream, []freebuffAccount{
		account("token-aaaaaaaaaaaaaaaa", "a@example.com"),
		account("token-bbbbbbbbbbbbbbbb", "b@example.com"),
	}, DefaultConfig())

	pool.pools[0].cool(time.Hour, "upstream_auth_rejected")

	lease, errAcquire := pool.Acquire(context.Background(), "base2-free")
	if errAcquire != nil {
		t.Fatalf("acquire with one cooling account: %v", errAcquire)
	}
	if lease.pool.name != "account-2" {
		t.Fatalf("expected account-2, got %s", lease.pool.name)
	}
}

func TestPoolReportsAllAccountsCooling(t *testing.T) {
	upstream := &fakeUpstream{sessionStatuses: []string{"active"}}
	pool, _ := testPool(t, upstream, []freebuffAccount{
		account("token-aaaaaaaaaaaaaaaa", "a@example.com"),
	}, DefaultConfig())
	pool.pools[0].cool(time.Hour, "upstream_auth_rejected")

	_, errAcquire := pool.Acquire(context.Background(), "base2-free")
	if errAcquire == nil {
		t.Fatal("expected an error when every account is cooling")
	}
	if errorClass(errAcquire) != "all_accounts_cooling" {
		t.Fatalf("error class = %s", errorClass(errAcquire))
	}
}

func TestPoolWaitingRoomPollsUntilActive(t *testing.T) {
	oldInterval := waitingRoomPollInterval
	waitingRoomPollInterval = time.Millisecond
	t.Cleanup(func() { waitingRoomPollInterval = oldInterval })

	upstream := &fakeUpstream{sessionStatuses: []string{"queued", "queued", "active"}}
	cfg := DefaultConfig()
	cfg.WaitingRoomTimeoutSeconds = 5
	pool, _ := testPool(t, upstream, []freebuffAccount{account("token-aaaaaaaaaaaaaaaa", "a@example.com")}, cfg)

	lease, errAcquire := pool.Acquire(context.Background(), "base2-free")
	if errAcquire != nil {
		t.Fatalf("acquire should survive a queued session: %v", errAcquire)
	}
	if lease.run == nil || lease.run.id == "" {
		t.Fatal("expected a run after the waiting room admitted the account")
	}
	if upstream.sessionCalls.Load() < 3 {
		t.Fatalf("expected the session to be polled at least three times, got %d", upstream.sessionCalls.Load())
	}
}

func TestPoolWaitingRoomTimeoutIsRetryableAndParksTheAccount(t *testing.T) {
	oldInterval := waitingRoomPollInterval
	waitingRoomPollInterval = time.Millisecond
	t.Cleanup(func() { waitingRoomPollInterval = oldInterval })

	upstream := &fakeUpstream{sessionStatuses: []string{"queued"}}
	cfg := DefaultConfig()
	// A one second budget keeps the test fast while still exercising the timeout branch.
	cfg.WaitingRoomTimeoutSeconds = 1
	pool, _ := testPool(t, upstream, []freebuffAccount{account("token-aaaaaaaaaaaaaaaa", "a@example.com")}, cfg)

	_, errAcquire := pool.Acquire(context.Background(), "base2-free")
	if errAcquire == nil {
		t.Fatal("expected a timeout error")
	}
	if errorClass(errAcquire) != "waiting_room_timeout" {
		t.Fatalf("error class = %s", errorClass(errAcquire))
	}
	if !isRetryable(errAcquire) {
		t.Fatal("a waiting-room timeout must be retryable")
	}
	if !pool.pools[0].cooling(time.Now()) {
		t.Fatal("the account should be parked after a waiting-room timeout")
	}
}

func TestPoolDisableCoolingStillReportsTheError(t *testing.T) {
	oldInterval := waitingRoomPollInterval
	waitingRoomPollInterval = time.Millisecond
	t.Cleanup(func() { waitingRoomPollInterval = oldInterval })

	upstream := &fakeUpstream{sessionStatuses: []string{"queued"}}
	cfg := DefaultConfig()
	cfg.WaitingRoomTimeoutSeconds = 1
	cfg.DisableCooling = true
	pool, _ := testPool(t, upstream, []freebuffAccount{account("token-aaaaaaaaaaaaaaaa", "a@example.com")}, cfg)

	_, errAcquire := pool.Acquire(context.Background(), "base2-free")
	if errAcquire == nil {
		t.Fatal("expected a timeout error")
	}
	if pool.pools[0].cooling(time.Now()) {
		t.Fatal("disable_cooling must stop the account being parked")
	}
}

func TestPoolSnapshotNeverCarriesTheToken(t *testing.T) {
	upstream := &fakeUpstream{sessionStatuses: []string{"active"}}
	secret := "token-verysecretvalue1234"
	pool, _ := testPool(t, upstream, []freebuffAccount{account(secret, "a@example.com")}, DefaultConfig())

	raw, errMarshal := json.Marshal(pool.Snapshot())
	if errMarshal != nil {
		t.Fatalf("marshal snapshot: %v", errMarshal)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the dashboard snapshot leaked an auth token")
	}
}

func TestClassifyUpstreamFailure(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCode  string
		retryable bool
	}{
		{"auth rejected", http.StatusUnauthorized, `{}`, "upstream_auth_rejected", true},
		{"rate limited", http.StatusTooManyRequests, `{}`, "upstream_rate_limited", true},
		{"waiting room", http.StatusConflict, `{"error":"waiting_room_queued"}`, "waiting_room_queued", true},
		{"session expired", http.StatusConflict, `{"error":"session_expired"}`, "session_invalid", true},
		{"bad request", http.StatusBadRequest, `{}`, "upstream_rejected_request", false},
		{"server error", http.StatusBadGateway, `{}`, "upstream_unavailable", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			errClassified := classifyUpstreamFailure(testCase.status, []byte(testCase.body))
			if errClassified.Code != testCase.wantCode {
				t.Fatalf("code = %s, want %s", errClassified.Code, testCase.wantCode)
			}
			if errClassified.Retryable != testCase.retryable {
				t.Fatalf("retryable = %v, want %v", errClassified.Retryable, testCase.retryable)
			}
		})
	}
}
