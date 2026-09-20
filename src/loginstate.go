package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pendingLogin is one started login flow. Freebuff issues tokens from a web page rather
// than an OAuth redirect, so the plugin cannot poll an authorisation endpoint: it watches
// the auth directory for the record the operator drops there.
type pendingLogin struct {
	startedAt time.Time
	expiresAt time.Time
}

type loginStore struct {
	mu      sync.Mutex
	pending map[string]pendingLogin
	now     func() time.Time
}

func newLoginStore() *loginStore {
	return &loginStore{pending: make(map[string]pendingLogin), now: time.Now}
}

func (s *loginStore) begin(ttl time.Duration) (string, time.Time, error) {
	state, errState := randomState()
	if errState != nil {
		return "", time.Time{}, errState
	}
	now := s.now()
	expiresAt := now.Add(ttl)
	s.mu.Lock()
	s.pending[state] = pendingLogin{startedAt: now, expiresAt: expiresAt}
	s.mu.Unlock()
	return state, expiresAt, nil
}

// lookup returns the flow for a state, rejecting an expired or unknown one.
func (s *loginStore) lookup(state string) (pendingLogin, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return pendingLogin{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.pending[state]
	if !found {
		return pendingLogin{}, false
	}
	if s.now().After(entry.expiresAt) {
		delete(s.pending, state)
		return pendingLogin{}, false
	}
	return entry, true
}

func (s *loginStore) finish(state string) {
	s.mu.Lock()
	delete(s.pending, strings.TrimSpace(state))
	s.mu.Unlock()
}

func randomState() (string, error) {
	buffer := make([]byte, 16)
	if _, errRead := rand.Read(buffer); errRead != nil {
		return "", errors.New("login state could not be generated")
	}
	return hex.EncodeToString(buffer), nil
}

// findLatestAuthFile returns the newest file in dir that parses as a Freebuff credential
// and was written at or after notBefore.
//
// It only reads files whose extension could plausibly be a credential, and it never
// returns file contents to a caller that logs them.
func findLatestAuthFile(dir string, notBefore time.Time) (Storage, string, bool, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Storage{}, "", false, errors.New("auth directory is not configured or could not be resolved")
	}
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return Storage{}, "", false, nil
		}
		return Storage{}, "", false, errRead
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	found := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".key") && !strings.HasSuffix(name, ".token") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		if !notBefore.IsZero() && info.ModTime().Before(notBefore.Add(-2*time.Second)) {
			continue
		}
		found = append(found, candidate{path: filepath.Join(dir, entry.Name()), modTime: info.ModTime()})
	}
	if len(found) == 0 {
		return Storage{}, "", false, nil
	}
	// Newest first: the operator just wrote it.
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].modTime.After(found[j-1].modTime); j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
	for _, item := range found {
		raw, errReadFile := readFileLimited(item.path)
		if errReadFile != nil {
			continue
		}
		storage, handled, errParse := ParseAuthFile(raw, item.path)
		if errParse != nil || !handled || !storage.Valid() {
			continue
		}
		return storage, item.path, true, nil
	}
	return Storage{}, "", false, nil
}
