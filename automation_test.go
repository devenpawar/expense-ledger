package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testGoogleJSON() json.RawMessage {
	return json.RawMessage(`{"web":{"client_id":"test-client.apps.googleusercontent.com","client_secret":"dummy-secret-for-tests","redirect_uris":["http://127.0.0.1:8080/api/auth/callback","http://127.0.0.1:8080/api/gmail/callback"],"token_uri":"https://attacker.invalid/token"}}`)
}

func TestDeveloperConfigurationPersistsAndNeverReturnsSecrets(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	dir := t.TempDir()
	g, err := newGmail(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	a.gmail = g
	s, err := newServer(context.Background(), dir, a)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"credentials": testGoogleJSON()})
	w := serverRequest(s, "POST", "/api/developer/google-config", string(b), nil, map[string]string{"X-Ledger-Owner": s.ownerKey})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !g.configured() {
		t.Fatal("import did not configure OAuth")
	}
	restored, err := newGmail(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.configured() || restored.clientID != "test-client.apps.googleusercontent.com" {
		t.Fatal("configuration did not survive restart")
	}
	authResponse := serverRequest(s, "POST", "/api/auth/google", "", nil, nil)
	if authResponse.Code != 200 {
		t.Fatal(authResponse.Body.String())
	}
	var result struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(authResponse.Body.Bytes(), &result)
	auth := result.URL
	u, _ := url.Parse(auth)
	if u.Host != "accounts.google.com" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("client_id") != restored.clientID {
		t.Fatal("unexpected OAuth authorization URL")
	}
	state := request(a, "GET", "/api/state", "", "", "127.0.0.1:8080", "")
	if strings.Contains(state.Body.String(), "dummy-secret-for-tests") || strings.Contains(state.Body.String(), "access_token") || strings.Contains(w.Body.String(), "dummy-secret-for-tests") {
		t.Fatal("credentials leaked in an API response")
	}
}

func TestInvalidDeveloperConfigurationKeepsExistingFile(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	dir := t.TempDir()
	g, err := newGmail(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.configure(testGoogleJSON()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "google-oauth.json"))
	for _, bad := range []string{`{"installed":{"client_id":"x","client_secret":"y"}}`, `{"web":{"client_id":"test.apps.googleusercontent.com","client_secret":"x","redirect_uris":["http://evil.example"]}}`, `{"web":{"client_id":"test.apps.googleusercontent.com","redirect_uris":["http://127.0.0.1:8080/api/gmail/callback"]}}`} {
		if g.configure(json.RawMessage(bad)) == nil {
			t.Fatal("invalid config accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "google-oauth.json"))
	if string(before) != string(after) {
		t.Fatal("invalid config overwrote working config")
	}
	g.token = Token{RefreshToken: "dummy"}
	if g.configure(testGoogleJSON()) == nil {
		t.Fatal("connected application's credentials were replaced")
	}
}

func TestAutomaticSyncRunsWithoutBrowserAndStopsAfterDisconnect(t *testing.T) {
	a := newTestApp(t)
	a.jobs = make(chan bool, 32)
	a.gmail.clientID = "test"
	a.gmail.secret = "test"
	a.gmail.path = filepath.Join(t.TempDir(), "gmail-token.json")
	a.gmail.token = Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}
	var syncs atomic.Int32
	done := make(chan struct{}, 10)
	a.gmail.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/profile") {
			return response(200, `{"emailAddress":"owner@example.com"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			syncs.Add(1)
			select {
			case done <- struct{}{}:
			default:
			}
			return response(200, `{"messages":[]}`), nil
		}
		return response(500, `{}`), nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); a.runAutomation(ctx, 30*time.Millisecond) }()
	defer func() { cancel(); <-stopped }()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("background sync did not run on startup and interval")
		}
	}
	// Acquire a quiet moment to exercise the real disconnect route.
	var code int
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		w := request(a, "POST", "/api/gmail/disconnect", "", "", "127.0.0.1:8080", "1")
		code = w.Code
		if code == 200 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if code != 200 {
		t.Fatal("could not disconnect background connection")
	}
	count := syncs.Load()
	a.queueGmail(true)
	time.Sleep(80 * time.Millisecond)
	if syncs.Load() != count || a.gmail.connected() {
		t.Fatal("Gmail was accessed after disconnect")
	}
	if a.syncStatus().LastError != "" {
		t.Fatal("disconnect left stale sync errors")
	}
}

func TestAutomaticSyncErrorIsVisibleAndRetryRecovers(t *testing.T) {
	a := newTestApp(t)
	a.gmail.token = Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}
	fail := true
	a.gmail.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if fail {
			return response(401, `{}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/profile") {
			return response(200, `{"emailAddress":"owner@example.com"}`), nil
		}
		return response(200, `{"messages":[]}`), nil
	})}
	if _, err := a.performSync(context.Background()); err == nil {
		t.Fatal("authorization failure was hidden")
	}
	if a.syncStatus().LastError == "" || a.syncStatus().Running {
		t.Fatal("failure did not update visible sync status")
	}
	fail = false
	if _, err := a.performSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.syncStatus().LastError != "" || a.store.snapshot().LastSync == "" {
		t.Fatal("successful retry did not clear the error")
	}
}
