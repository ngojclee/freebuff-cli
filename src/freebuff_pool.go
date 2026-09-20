package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// authCooldown is how long an account is parked after the upstream rejects it. It
	// matches the reference implementation's 30 minute penalty.
	authCooldown = 30 * time.Minute

	// waitingRoomCooldown is deliberately short. A queued account is not broken, it is
	// busy, so parking it briefly lets another account serve the request without hiding
	// the account for long.
	waitingRoomCooldown = 60 * time.Second
)

// waitingRoomPollInterval is how often the pool re-checks a queued session. It is a
// variable rather than a constant so tests can shorten it without changing behaviour.
var waitingRoomPollInterval = 5 * time.Second

// agentRun is one open upstream agent run. A run is reused across requests for the same
// agent until it is rotated, so the plugin does not open a run per request.
type agentRun struct {
	id        string
	agentID   string
	startedAt time.Time
	requests  int
	inflight  int
}

// accountPool is the per-account state: one credential, its waiting-room session, its
// open runs, and its cooldown.
type accountPool struct {
	name  string
	label string
	token string

	cfg    Config
	client *freebuffClient
	now    func() time.Time

	mu             sync.Mutex
	runs           map[string]*agentRun
	session        *freebuffSession
	cooldownUntil  time.Time
	lastErrorClass string
	lastErrorAt    time.Time
	lastSuccessAt  time.Time
}

// lease is a reserved account plus the run the caller must use.
type lease struct {
	pool *accountPool
	run  *agentRun
}

// tokenPool rotates requests across accounts. It never stores a token anywhere except in
// the accountPool that owns it, and no method returns one.
type tokenPool struct {
	cfg    Config
	client *freebuffClient
	now    func() time.Time
	pools  []*accountPool
	next   atomic.Uint64
}

func newTokenPool(cfg Config, client *freebuffClient, accounts []freebuffAccount, now func() time.Time) *tokenPool {
	if now == nil {
		now = time.Now
	}
	pools := make([]*accountPool, 0, len(accounts))
	for index, account := range accounts {
		if strings.TrimSpace(account.AuthToken) == "" || !account.Enabled {
			continue
		}
		pools = append(pools, &accountPool{
			name:   fmt.Sprintf("account-%d", index+1),
			label:  account.label(),
			token:  account.AuthToken,
			cfg:    cfg,
			client: client,
			now:    now,
			runs:   make(map[string]*agentRun),
		})
	}
	return &tokenPool{cfg: cfg, client: client, now: now, pools: pools}
}

// Accounts reports how many usable accounts the pool holds.
func (p *tokenPool) Accounts() int {
	if p == nil {
		return 0
	}
	return len(p.pools)
}

// Acquire reserves an account and an agent run for one request.
//
// Selection is round-robin over accounts that are not cooling, so a healthy account is
// always preferred and a parked account is skipped without a per-request probe.
func (p *tokenPool) Acquire(ctx context.Context, agentID string) (*lease, error) {
	if p == nil || len(p.pools) == 0 {
		return nil, newFreebuffError("no_accounts", "no Freebuff account is configured", false, 0)
	}
	start := int(p.next.Add(1)-1) % len(p.pools)
	now := p.now()

	var lastErr error
	considered := 0
	for offset := 0; offset < len(p.pools); offset++ {
		account := p.pools[(start+offset)%len(p.pools)]
		if account.cooling(now) {
			continue
		}
		considered++
		run, errAcquire := account.acquireRun(ctx, agentID)
		if errAcquire == nil {
			return &lease{pool: account, run: run}, nil
		}
		lastErr = errAcquire
		if !isRetryable(errAcquire) {
			return nil, errAcquire
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if considered == 0 {
		return nil, newFreebuffError("all_accounts_cooling", "every Freebuff account is cooling down", true, 0)
	}
	return nil, newFreebuffError("no_account_available", "no Freebuff account could serve the request", true, 0)
}

// Release returns an account to the pool after a successful exchange.
func (p *tokenPool) Release(l *lease) {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.releaseRun(l.run)
}

// Cooldown parks an account for d. The reason is reduced to a short class so nothing
// sensitive can reach the dashboard or the log.
func (p *tokenPool) Cooldown(l *lease, d time.Duration, reason string) {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.cool(d, reason)
}

// Invalidate drops the cached session and run for an account so the next request starts
// fresh. It is used when the upstream says the run or session is no longer valid.
func (p *tokenPool) Invalidate(l *lease, reason string) {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.invalidate(l.run)
	l.pool.noteError(reason)
}

// Snapshot is the redacted dashboard view. It never carries a token.
func (p *tokenPool) Snapshot() []map[string]any {
	if p == nil {
		return nil
	}
	now := p.now()
	out := make([]map[string]any, 0, len(p.pools))
	for _, account := range p.pools {
		out = append(out, account.snapshot(now))
	}
	return out
}

func (a *accountPool) cooling(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return now.Before(a.cooldownUntil)
}

// cool parks the account, unless the operator disabled cooling. The check lives here
// rather than only on tokenPool.Cooldown so the waiting-room timeout path honours it too.
func (a *accountPool) cool(d time.Duration, reason string) {
	if a.cfg.DisableCooling {
		a.noteError(reason)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	until := a.now().Add(d)
	if until.After(a.cooldownUntil) {
		a.cooldownUntil = until
	}
	a.lastErrorClass = redactReason(reason)
	a.lastErrorAt = a.now()
}

func (a *accountPool) noteError(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastErrorClass = redactReason(reason)
	a.lastErrorAt = a.now()
}

func (a *accountPool) noteSuccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSuccessAt = a.now()
	a.lastErrorClass = ""
}

// instanceID returns the waiting-room instance id of the ready session, if any. It is not
// a credential; it is the token the upstream uses to identify this session.
func (a *accountPool) instanceID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return ""
	}
	return a.session.InstanceID
}

// authToken returns the account credential. It exists so the executor can call the
// upstream without the pool ever handing a token to a logger or a response body.
func (a *accountPool) authToken() string {
	return a.token
}

// acquireRun returns the current run for an agent, opening one when needed. The session
// is ensured first: a queued account must never be handed to a caller.
func (a *accountPool) acquireRun(ctx context.Context, agentID string) (*agentRun, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, newFreebuffError("unknown_model", "no free agent serves this model", false, 0)
	}

	if errSession := a.ensureSession(ctx); errSession != nil {
		return nil, errSession
	}

	a.mu.Lock()
	existing := a.runs[agentID]
	if existing != nil && !a.runExpiredLocked(existing) {
		existing.inflight++
		a.mu.Unlock()
		return existing, nil
	}
	// An expired run is closed out of band; the caller gets a fresh one.
	stale := existing
	a.mu.Unlock()

	if stale != nil {
		go a.finishRun(stale)
	}

	runID, errStart := a.client.StartRun(ctx, a.token, agentID)
	if errStart != nil {
		a.noteError(errorClass(errStart))
		return nil, errStart
	}

	run := &agentRun{id: runID, agentID: agentID, startedAt: a.now(), inflight: 1}
	a.mu.Lock()
	a.runs[agentID] = run
	a.mu.Unlock()
	return run, nil
}

func (a *accountPool) releaseRun(run *agentRun) {
	if run == nil {
		return
	}
	a.mu.Lock()
	if run.inflight > 0 {
		run.inflight--
	}
	run.requests++
	a.mu.Unlock()
	a.noteSuccess()
}

// runExpiredLocked reports whether a run has passed the rotation interval. Callers must
// hold a.mu.
func (a *accountPool) runExpiredLocked(run *agentRun) bool {
	if run == nil {
		return true
	}
	interval := time.Duration(a.cfg.RotationIntervalSeconds) * time.Second
	return a.now().Sub(run.startedAt) >= interval
}

func (a *accountPool) finishRun(run *agentRun) {
	if run == nil || run.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = a.client.FinishRun(ctx, a.token, run.id, run.requests)
}

func (a *accountPool) invalidate(run *agentRun) {
	a.mu.Lock()
	if run != nil {
		delete(a.runs, run.agentID)
	}
	a.session = nil
	a.mu.Unlock()
	if run != nil {
		go a.finishRun(run)
	}
}

// ensureSession returns the instance id for a ready session, joining or polling the
// waiting room as needed.
//
// This is the bounded-wait behaviour the owner asked for: poll until the account gets a
// slot or the configured timeout elapses. On timeout the account is parked briefly and a
// typed retryable error is returned, so CPA's scheduler skips it and the caller can retry.
func (a *accountPool) ensureSession(ctx context.Context) error {
	deadline := a.now().Add(time.Duration(a.cfg.WaitingRoomTimeoutSeconds) * time.Second)

	for {
		a.mu.Lock()
		session := a.session
		a.mu.Unlock()

		switch sessionReady(session, a.now()) {
		case sessionReadyState:
			return nil
		case sessionDisabledState:
			return nil
		}

		next, errSession := a.refreshSession(ctx, session)
		if errSession != nil {
			a.mu.Lock()
			a.session = nil
			a.mu.Unlock()
			a.noteError(errorClass(errSession))
			return errSession
		}

		a.mu.Lock()
		a.session = &next
		a.mu.Unlock()

		switch next.Status {
		case sessionStatusDisabled, sessionStatusActive:
			return nil
		case sessionStatusEnded, sessionStatusSuperseded:
			// Drop it and try once more from scratch.
			a.mu.Lock()
			a.session = nil
			a.mu.Unlock()
			continue
		}

		if a.now().After(deadline) {
			reason := "waiting_room_timeout"
			a.cool(waitingRoomCooldown, reason)
			return newFreebuffError(reason, "the free-tier waiting room did not admit this account in time", true, 0)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitingRoomPollInterval):
		}
	}
}

func (a *accountPool) refreshSession(ctx context.Context, cached *freebuffSession) (freebuffSession, error) {
	if cached != nil && cached.InstanceID != "" {
		session, errGet := a.client.GetSession(ctx, a.token, cached.InstanceID)
		if errGet == nil {
			return session, nil
		}
		if !isSessionInvalid(errGet) {
			return freebuffSession{}, errGet
		}
	}
	return a.client.CreateOrRefreshSession(ctx, a.token)
}

type sessionReadiness int

const (
	sessionNotReadyState sessionReadiness = iota
	sessionReadyState
	sessionDisabledState
)

// sessionReady decides whether a cached session can serve traffic right now.
func sessionReady(session *freebuffSession, now time.Time) sessionReadiness {
	if session == nil {
		return sessionNotReadyState
	}
	switch session.Status {
	case sessionStatusDisabled:
		return sessionDisabledState
	case sessionStatusActive:
		if session.InstanceID == "" {
			return sessionNotReadyState
		}
		if session.ExpiresAt.IsZero() || now.Before(session.ExpiresAt.Add(-5*time.Second)) {
			return sessionReadyState
		}
	}
	return sessionNotReadyState
}

func (a *accountPool) snapshot(now time.Time) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	runs := make([]map[string]any, 0, len(a.runs))
	for _, run := range a.runs {
		runs = append(runs, map[string]any{
			"agent_id":   run.agentID,
			"run_id":     run.id,
			"started_at": run.startedAt.UTC().Format(time.RFC3339),
			"requests":   run.requests,
			"inflight":   run.inflight,
		})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i]["agent_id"].(string) < runs[j]["agent_id"].(string)
	})

	out := map[string]any{
		"name":             a.name,
		"label":            a.label,
		"state":            "active",
		"runs":             runs,
		"last_error":       a.lastErrorClass,
		"cooling":          now.Before(a.cooldownUntil),
		"cooldown_until":   "",
		"last_error_at":    "",
		"last_success_at":  "",
		"session_status":   string(sessionStatusNone),
		"session_position": 0,
		"session_queue":    0,
		"session_expires":  "",
	}
	if now.Before(a.cooldownUntil) {
		out["state"] = "cooldown"
		out["cooldown_until"] = a.cooldownUntil.UTC().Format(time.RFC3339)
	}
	if a.session != nil {
		out["session_status"] = string(a.session.Status)
		out["session_position"] = a.session.Position
		out["session_queue"] = a.session.QueueDepth
		if !a.session.ExpiresAt.IsZero() {
			out["session_expires"] = a.session.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	if !a.lastErrorAt.IsZero() {
		out["last_error_at"] = a.lastErrorAt.UTC().Format(time.RFC3339)
	}
	if !a.lastSuccessAt.IsZero() {
		out["last_success_at"] = a.lastSuccessAt.UTC().Format(time.RFC3339)
	}
	return out
}

// errorClass reduces any error to a short, loggable class.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	if freebuffErr, ok := err.(*freebuffError); ok {
		return freebuffErr.Code
	}
	return "unexpected_error"
}

// isRetryable reports whether another account could plausibly serve the request.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if freebuffErr, ok := err.(*freebuffError); ok {
		return freebuffErr.Retryable
	}
	return true
}
