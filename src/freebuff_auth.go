package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var freebuffAuthOrigin = "https://freebuff.llm.pm"

var freebuffAuthClient = &http.Client{Timeout: 20 * time.Second}

type freebuffLoginSession struct {
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
	LoginURL        string `json:"loginUrl"`
	ExpiresAt       int64  `json:"expiresAt"`
}

type freebuffLoginUser struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	AuthToken       string `json:"authToken"`
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
}

type freebuffLoginStatus struct {
	Pending bool               `json:"pending"`
	User    *freebuffLoginUser `json:"user"`
	Error   string             `json:"error"`
}

// startFreebuffLogin creates the vendor's browser session and returns the URL the
// operator opens. The token itself stays on the vendor side until pollFreebuffLogin
// observes a completed sign-in.
func startFreebuffLogin(ctx context.Context) (freebuffLoginSession, error) {
	var session freebuffLoginSession
	if errPost := freebuffPostJSON(ctx, freebuffAuthOrigin+"/api/code", map[string]any{}, &session); errPost != nil {
		return freebuffLoginSession{}, errPost
	}
	if strings.TrimSpace(session.FingerprintID) == "" ||
		strings.TrimSpace(session.FingerprintHash) == "" ||
		strings.TrimSpace(session.LoginURL) == "" {
		return freebuffLoginSession{}, errors.New("freebuff login response is incomplete")
	}
	return session, nil
}

func pollFreebuffLogin(ctx context.Context, session freebuffLoginSession) (freebuffLoginStatus, error) {
	payload := map[string]any{
		"fingerprintId":   session.FingerprintID,
		"fingerprintHash": session.FingerprintHash,
		"expiresAt":       session.ExpiresAt,
	}
	var status freebuffLoginStatus
	if errPost := freebuffPostJSON(ctx, freebuffAuthOrigin+"/api/status", payload, &status); errPost != nil {
		return freebuffLoginStatus{}, errPost
	}
	return status, nil
}

func freebuffPostJSON(ctx context.Context, endpoint string, payload any, out any) error {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errors.New("freebuff request could not be encoded")
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		return errors.New("freebuff request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, errDo := freebuffAuthClient.Do(request)
	if errDo != nil {
		return fmt.Errorf("freebuff auth endpoint is unreachable: %w", errDo)
	}
	defer response.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if errRead != nil {
		return errors.New("freebuff auth response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("freebuff auth endpoint returned %d", response.StatusCode)
	}
	if errUnmarshal := json.Unmarshal(raw, out); errUnmarshal != nil {
		return errors.New("freebuff auth response could not be decoded")
	}
	return nil
}
