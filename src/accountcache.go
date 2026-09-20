package main

import (
	"sort"
	"strings"
	"sync"
)

// accountCache remembers every Freebuff account the plugin has seen, so a request can
// fall over to a sibling account when the selected one is cooling or queued.
//
// CPA's own scheduler already rotates across auth records; this cache exists because a
// retryable failure (a rejected token, a waiting-room timeout) has to be handled inside
// the request that hit it. The cache holds tokens in memory only, never on disk beyond
// the auth file CPA owns, and it never exposes them to a caller that logs.
type accountCache struct {
	mu       sync.Mutex
	accounts map[string]freebuffAccount
}

func newAccountCache() *accountCache {
	return &accountCache{accounts: make(map[string]freebuffAccount)}
}

var accountStore = newAccountCache()

func (c *accountCache) remember(storage Storage) {
	account := storage.Account()
	if !account.Enabled {
		return
	}
	key := storage.AuthID()
	c.mu.Lock()
	c.accounts[key] = account
	c.mu.Unlock()
}

func (c *accountCache) forget(authID string) {
	c.mu.Lock()
	delete(c.accounts, authID)
	c.mu.Unlock()
}

// list returns the known accounts with preferredAuthID first, so the caller's selected
// credential is tried before any sibling.
func (c *accountCache) list(preferredAuthID string) []freebuffAccount {
	c.mu.Lock()
	keys := make([]string, 0, len(c.accounts))
	for key := range c.accounts {
		keys = append(keys, key)
	}
	c.mu.Unlock()
	sort.Strings(keys)

	ordered := make([]freebuffAccount, 0, len(keys))
	if preferred := strings.TrimSpace(preferredAuthID); preferred != "" {
		c.mu.Lock()
		if account, found := c.accounts[preferred]; found {
			ordered = append(ordered, account)
		}
		c.mu.Unlock()
	}
	for _, key := range keys {
		if key == preferredAuthID {
			continue
		}
		c.mu.Lock()
		account, found := c.accounts[key]
		c.mu.Unlock()
		if found {
			ordered = append(ordered, account)
		}
	}
	return ordered
}

// size reports how many distinct accounts the plugin knows about.
func (c *accountCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.accounts)
}
