package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serverRequest(s *Server, method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8080"
	r.Header.Set("X-Ledger-Request", "1")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

type fakeGrant struct {
	Identity     Identity
	Nonce, Scope string
}
type fakeGoogle struct {
	key           *rsa.PrivateKey
	grants        map[string]fakeGrant
	emails        map[string]string
	mailRequests  int
	tokenRequests int
}

func testSSOServer(t *testing.T) (*Server, *fakeGoogle) {
	t.Helper()
	a := newTestApp(t)
	a.gmail.clientID = "test-client.apps.googleusercontent.com"
	a.gmail.secret = "dummy"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGoogle{key: key, grants: map[string]fakeGrant{}, emails: map[string]string{}}
	a.gmail.client = &http.Client{Transport: roundTrip(fake.serve)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	a.gmail.configPath = filepath.Join(dir, "google-oauth.json")
	s, err := newServer(ctx, dir, a)
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func signID(key *rsa.PrivateKey, claims any) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	b, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(b)
	text := head + "." + body
	hash := sha256.Sum256([]byte(text))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	return text + "." + base64.RawURLEncoding.EncodeToString(signature)
}
func testClaims(p Identity, nonce string) map[string]any {
	return map[string]any{"iss": "https://accounts.google.com", "aud": "test-client.apps.googleusercontent.com", "sub": p.Subject, "name": p.Name, "email": p.Email, "email_verified": true, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}
}
func (f *fakeGoogle) serve(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/token":
		f.tokenRequests++
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		g, ok := f.grants[values.Get("code")]
		if !ok {
			return response(400, `{}`), nil
		}
		f.emails[g.Identity.Subject] = g.Identity.Email
		b, _ := json.Marshal(Token{AccessToken: "access-" + g.Identity.Subject, RefreshToken: "refresh-" + g.Identity.Subject, ExpiresIn: 3600, IDToken: signID(f.key, testClaims(g.Identity, g.Nonce)), Scope: g.Scope})
		return response(200, string(b)), nil
	case "/oauth2/v3/certs":
		b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
		return response(200, string(b)), nil
	case "/gmail/v1/users/me/profile":
		f.mailRequests++
		subject := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer access-")
		b, _ := json.Marshal(map[string]string{"emailAddress": f.emails[subject]})
		return response(200, string(b)), nil
	case "/gmail/v1/users/me/messages":
		f.mailRequests++
		return response(200, `{"messages":[]}`), nil
	}
	return response(500, `{}`), nil
}

func beginTestFlow(t *testing.T, s *Server, kind, account string, session []*http.Cookie) (*url.URL, []*http.Cookie) {
	t.Helper()
	path := "/api/auth/google"
	body := ""
	if kind == "gmail" {
		path = "/api/gmail/connect"
		body = `{"account":"` + account + `"}`
	}
	w := serverRequest(s, "POST", path, body, session, nil)
	if w.Code != 200 {
		t.Fatalf("begin failed %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	u, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, w.Result().Cookies()
}
func finishTestFlow(t *testing.T, s *Server, f *fakeGoogle, u *url.URL, p Identity, cookies []*http.Cookie, session []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	state := u.Query().Get("state")
	f.grants[state] = fakeGrant{p, u.Query().Get("nonce"), u.Query().Get("scope")}
	callback, _ := url.Parse(u.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"state": {state}, "code": {state}}.Encode()
	return serverRequest(s, "GET", callback.RequestURI(), "", append(append([]*http.Cookie{}, cookies...), session...), nil)
}
func loginTestUser(t *testing.T, s *Server, f *fakeGoogle, p Identity) []*http.Cookie {
	t.Helper()
	u, cookies := beginTestFlow(t, s, "login", "", nil)
	w := finishTestFlow(t, s, f, u, p, cookies, nil)
	if w.Code != 303 || w.Header().Get("Location") != "/?signed_in=1" {
		t.Fatalf("login failed: %d %s", w.Code, w.Header().Get("Location"))
	}
	var sessions []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sessions = append(sessions, c)
		}
	}
	if len(sessions) != 1 || !sessions[0].HttpOnly || sessions[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing secure app session cookie")
	}
	return sessions
}

var alice = Identity{"google-alice", "alice@gmail.com", "Alice"}
var bob = Identity{"google-bob", "bob@gmail.com", "Bob"}

func TestPrivateAPIsRequireSignIn(t *testing.T) {
	s, _ := testSSOServer(t)
	for _, c := range []struct{ method, path string }{{"GET", "/api/state"}, {"GET", "/api/export"}, {"POST", "/api/transactions"}, {"POST", "/api/categories"}, {"PATCH", "/api/categories"}, {"DELETE", "/api/categories"}, {"POST", "/api/parse"}, {"POST", "/api/gmail/connect"}, {"POST", "/api/gmail/sync"}, {"POST", "/api/onboarding/skip"}} {
		w := serverRequest(s, c.method, c.path, `{}`, nil, map[string]string{"X-User-ID": "google-alice"})
		if w.Code != 401 {
			t.Fatalf("anonymous %s %s got %d", c.method, c.path, w.Code)
		}
	}
	w := serverRequest(s, "GET", "/api/session", "", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":null`) || strings.Contains(w.Body.String(), "transactions") {
		t.Fatal("public bootstrap exposed private records")
	}
}

func TestSSOAndInboxConsentAreSeparate(t *testing.T) {
	s, f := testSSOServer(t)
	u, cookies := beginTestFlow(t, s, "login", "", nil)
	q := u.Query()
	if q.Get("scope") != "openid email profile" || strings.Contains(q.Get("scope"), "gmail") || q.Get("access_type") == "offline" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
		t.Fatal("login requested inbox access or omitted flow protections")
	}
	w := finishTestFlow(t, s, f, u, alice, cookies, nil)
	var session []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			session = append(session, c)
		}
	}
	if len(session) != 1 || f.mailRequests != 0 {
		t.Fatal("sign-in accessed Gmail")
	}
	w = serverRequest(s, "GET", "/api/session", "", session, nil)
	if !strings.Contains(w.Body.String(), `"choiceComplete":false`) || !strings.Contains(w.Body.String(), alice.Email) {
		t.Fatal("sign-in did not lead to inbox selection")
	}
	u, _ = beginTestFlow(t, s, "gmail", "same", session)
	if u.Query().Get("login_hint") != alice.Email || !strings.Contains(u.Query().Get("scope"), gmailScope) || u.Query().Get("access_type") != "offline" {
		t.Fatal("same-email consent was not configured")
	}
	u, _ = beginTestFlow(t, s, "gmail", "another", session)
	if u.Query().Get("login_hint") != "" || !strings.Contains(u.Query().Get("prompt"), "select_account") {
		t.Fatal("other account chooser missing")
	}
}

func TestAnotherInboxDoesNotChangeSignInIdentity(t *testing.T) {
	s, f := testSSOServer(t)
	session := loginTestUser(t, s, f, alice)
	u, cookies := beginTestFlow(t, s, "gmail", "same", session)
	w := finishTestFlow(t, s, f, u, bob, cookies, session)
	if !strings.Contains(w.Header().Get("Location"), "gmail_error=") {
		t.Fatal("same-email mode accepted another identity")
	}
	a := s.users[fingerprint("google:"+alice.Subject)].App
	if a.gmail.connected() {
		t.Fatal("rejected account was persisted")
	}
	u, cookies = beginTestFlow(t, s, "gmail", "another", session)
	w = finishTestFlow(t, s, f, u, bob, cookies, session)
	if w.Header().Get("Location") != "/?gmail=connected" {
		t.Fatal(w.Header().Get("Location"))
	}
	if !a.gmail.connected() || a.store.snapshot().GmailAccount != bob.Email {
		t.Fatal("selected inbox was not connected")
	}
	public := serverRequest(s, "GET", "/api/session", "", session, nil)
	var state struct {
		User  struct{ Email string }
		Inbox struct {
			Email     string
			Connected bool
		}
	}
	_ = json.Unmarshal(public.Body.Bytes(), &state)
	if state.User.Email != alice.Email || state.Inbox.Email != bob.Email || !state.Inbox.Connected {
		t.Fatal("inbox selection overwrote app sign-in identity")
	}
	b, _ := os.ReadFile(a.gmail.path)
	if strings.Contains(string(b), "id_token") {
		t.Fatal("identity JWT stored with Gmail token")
	}
}

func TestUserLedgersAndSessionsAreIsolated(t *testing.T) {
	s, f := testSSOServer(t)
	first := loginTestUser(t, s, f, alice)
	second := loginTestUser(t, s, f, bob)
	entry, _ := json.Marshal(sample())
	w := serverRequest(s, "POST", "/api/transactions", string(entry), first, nil)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w := serverRequest(s, "POST", "/api/categories", `{"name":"Alice only"}`, first, nil); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	own := serverRequest(s, "GET", "/api/state", "", first, nil)
	other := serverRequest(s, "GET", "/api/state", "", second, nil)
	if !strings.Contains(own.Body.String(), "Amazon") || !strings.Contains(own.Body.String(), "Alice only") || strings.Contains(other.Body.String(), "Amazon") || strings.Contains(other.Body.String(), "Alice only") {
		t.Fatal("ledger data or labels crossed user boundary")
	}
	csv := serverRequest(s, "GET", "/api/export", "", second, nil)
	if strings.Contains(csv.Body.String(), "Amazon") {
		t.Fatal("CSV leaked another user's records")
	}
	patch := `{"merchant":"Hacked","category":"Other","date":"2026-10-20","amount":50000,"note":"","status":"accepted","allowDuplicate":true}`
	if serverRequest(s, "PATCH", "/api/transactions/"+created.ID, patch, second, nil).Code != 404 {
		t.Fatal("one user edited another user's transaction")
	}
	if s.users[fingerprint("google:"+alice.Subject)].App.store.path == s.users[fingerprint("google:"+bob.Subject)].App.store.path {
		t.Fatal("user storage paths are shared")
	}
	serverRequest(s, "POST", "/api/auth/logout", "", first, nil)
	if serverRequest(s, "GET", "/api/state", "", first, nil).Code != 401 || serverRequest(s, "GET", "/api/state", "", second, nil).Code != 200 {
		t.Fatal("logout did not revoke exactly its own session")
	}
}

func TestCallbackIsBoundToBrowserAndSession(t *testing.T) {
	s, f := testSSOServer(t)
	u, _ := beginTestFlow(t, s, "login", "", nil)
	w := finishTestFlow(t, s, f, u, alice, nil, nil)
	if !strings.Contains(w.Header().Get("Location"), "auth_error=") || f.tokenRequests != 0 {
		t.Fatal("login callback accepted without browser state cookie")
	}
	session := loginTestUser(t, s, f, alice)
	u, cookies := beginTestFlow(t, s, "gmail", "another", session)
	serverRequest(s, "POST", "/api/auth/logout", "", session, nil)
	other := loginTestUser(t, s, f, bob)
	w = finishTestFlow(t, s, f, u, alice, cookies, other)
	if !strings.Contains(w.Header().Get("Location"), "gmail_error=") || s.users[fingerprint("google:"+bob.Subject)].App.gmail.connected() {
		t.Fatal("stale callback connected inbox to another session")
	}
}

func TestSessionExpiryAndOnboardingSkip(t *testing.T) {
	s, f := testSSOServer(t)
	session := loginTestUser(t, s, f, alice)
	if serverRequest(s, "POST", "/api/onboarding/skip", "", session, nil).Code != 200 {
		t.Fatal("could not choose manual entry")
	}
	state := serverRequest(s, "GET", "/api/session", "", session, nil)
	if !strings.Contains(state.Body.String(), `"choiceComplete":true`) {
		t.Fatal("onboarding choice was not saved")
	}
	s.mu.Lock()
	id := fingerprint(session[0].Value)
	expired := s.sessions[id]
	expired.Expires = time.Now().Add(-time.Second)
	s.sessions[id] = expired
	s.mu.Unlock()
	if serverRequest(s, "GET", "/api/state", "", session, nil).Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestDeveloperSetupRequiresOwnerKey(t *testing.T) {
	s, _ := testSSOServer(t)
	b, _ := json.Marshal(map[string]any{"credentials": testGoogleJSON()})
	if serverRequest(s, "POST", "/api/developer/google-config", string(b), nil, nil).Code != 403 {
		t.Fatal("configuration changed without owner key")
	}
	if serverRequest(s, "POST", "/api/developer/google-config", string(b), nil, map[string]string{"X-Ledger-Owner": s.ownerKey}).Code != 200 {
		t.Fatal("owner could not configure app")
	}
}

func TestIDTokenVerificationRejectsInvalidClaimsAndSignature(t *testing.T) {
	_, f := testSSOServer(t)
	client := &http.Client{Transport: roundTrip(f.serve)}
	v := &IDVerifier{}
	nonce := "expected-nonce"
	id := "test-client.apps.googleusercontent.com"
	raw := signID(f.key, testClaims(alice, nonce))
	if _, err := v.verify(context.Background(), client, raw, id, nonce); err != nil {
		t.Fatal(err)
	}
	cases := map[string]any{"iss": "https://attacker.invalid", "aud": "other-client", "email_verified": false, "exp": time.Now().Add(-time.Minute).Unix(), "nonce": "wrong-nonce", "iat": time.Now().Add(time.Hour).Unix(), "sub": ""}
	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			claims := testClaims(alice, nonce)
			claims[field] = value
			if _, err := v.verify(context.Background(), client, signID(f.key, claims), id, nonce); err == nil {
				t.Fatal("invalid claim accepted")
			}
		})
	}
	parts := strings.Split(raw, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`))
	if _, err := v.verify(context.Background(), client, strings.Join(parts, "."), id, nonce); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestStableGoogleSubjectRestoresLedgerAfterRestart(t *testing.T) {
	s, f := testSSOServer(t)
	session := loginTestUser(t, s, f, alice)
	entry, _ := json.Marshal(sample())
	if w := serverRequest(s, "POST", "/api/transactions", string(entry), session, nil); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	// Older unassigned records must never become the next visitor's expenses.
	if err := s.root.store.update(func(d *Database) error {
		x := sample()
		x.Merchant = "Legacy unassigned"
		addTransaction(d, x, "legacy")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := newServer(s.ctx, s.dir, s.root)
	if err != nil {
		t.Fatal(err)
	}
	if serverRequest(restarted, "GET", "/api/state", "", session, nil).Code != 401 {
		t.Fatal("restart reused an expired in-memory browser session")
	}
	changed := alice
	changed.Email = "changed-address@gmail.com"
	session = loginTestUser(t, restarted, f, changed)
	own := serverRequest(restarted, "GET", "/api/state", "", session, nil)
	if !strings.Contains(own.Body.String(), "Amazon") || strings.Contains(own.Body.String(), "Legacy unassigned") {
		t.Fatal("identity email change lost the ledger or exposed unassigned data")
	}
	if len(s.root.store.snapshot().Transactions) != 1 {
		t.Fatal("legacy records were modified")
	}
}

func TestDisconnectInvalidatesPendingInboxConsent(t *testing.T) {
	s, f := testSSOServer(t)
	session := loginTestUser(t, s, f, alice)
	u, cookies := beginTestFlow(t, s, "gmail", "another", session)
	if w := serverRequest(s, "POST", "/api/gmail/disconnect", "", session, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := f.tokenRequests
	w := finishTestFlow(t, s, f, u, bob, cookies, session)
	if !strings.Contains(w.Header().Get("Location"), "gmail_error=") || f.tokenRequests != before {
		t.Fatal("a pending consent flow survived disconnect")
	}
	if s.users[fingerprint("google:"+alice.Subject)].App.gmail.connected() {
		t.Fatal("disconnect was undone by a late callback")
	}
}
