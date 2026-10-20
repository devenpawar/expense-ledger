package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "ledger_session"
const sessionLifetime = 12 * time.Hour

type userAccount struct {
	Profile Identity
	App     *App
	handler http.Handler
}
type userSession struct {
	UserID  string
	Expires time.Time
}
type authFlow struct {
	Kind, Verifier, Nonce, UserID, Session, ExpectedEmail, ClientID string
	Expires                                                         time.Time
}
type Server struct {
	mu       sync.Mutex
	root     *App
	dir      string
	ownerKey string
	users    map[string]*userAccount
	sessions map[string]userSession
	flows    map[string]authFlow
	verifier IDVerifier
	ctx      context.Context
	started  bool
}

func newServer(ctx context.Context, dir string, root *App) (*Server, error) {
	// The root Gmail object is an OAuth provider only. Legacy tokens remain on disk
	// and are never assigned to whichever person signs in first.
	root.gmail.mu.Lock()
	root.gmail.token = Token{}
	root.gmail.mu.Unlock()
	s := &Server{root: root, dir: dir, users: map[string]*userAccount{}, sessions: map[string]userSession{}, flows: map[string]authFlow{}, ctx: ctx}
	keyPath := filepath.Join(dir, "developer-key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		s.ownerKey = randomID()
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err = os.WriteFile(keyPath, []byte(s.ownerKey), 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		s.ownerKey = strings.TrimSpace(string(key))
		if len(s.ownerKey) < 32 {
			return nil, fmt.Errorf("invalid local developer key; file preserved")
		}
	}
	dirs, err := os.ReadDir(filepath.Join(dir, "users"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, e := os.ReadFile(filepath.Join(dir, "users", d.Name(), "profile.json"))
		if e != nil {
			return nil, e
		}
		var p Identity
		if json.Unmarshal(b, &p) != nil || fingerprint("google:"+p.Subject) != d.Name() {
			return nil, fmt.Errorf("invalid saved user profile; data preserved")
		}
		if _, e = s.openUserLocked(p); e != nil {
			return nil, e
		}
	}
	return s, nil
}

func (s *Server) openUserLocked(p Identity) (*userAccount, error) {
	id := fingerprint("google:" + p.Subject)
	if u := s.users[id]; u != nil {
		if err := atomicJSON(filepath.Join(s.dir, "users", id, "profile.json"), p); err != nil {
			return nil, err
		}
		u.Profile = p
		return u, nil
	}
	dir := filepath.Join(s.dir, "users", id)
	store, err := openStore(filepath.Join(dir, "ledger.json"))
	if err != nil {
		return nil, err
	}
	root := s.root.gmail
	root.mu.Lock()
	g := &Gmail{clientID: root.clientID, secret: root.secret, path: filepath.Join(dir, "gmail-token.json"), client: root.client, configPath: root.configPath, configFromEnvironment: root.configFromEnvironment}
	root.mu.Unlock()
	b, err := os.ReadFile(g.path)
	if err == nil {
		if json.Unmarshal(b, &g.token) != nil {
			return nil, fmt.Errorf("invalid saved Gmail token; data preserved")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = atomicJSON(filepath.Join(dir, "profile.json"), p); err != nil {
		return nil, err
	}
	a := &App{store: store, gmail: g, jobs: make(chan bool, 32)}
	u := &userAccount{Profile: p, App: a, handler: a.routes()}
	s.users[id] = u
	if s.started {
		go a.runAutomation(s.ctx, syncInterval)
	}
	return u, nil
}

func (s *Server) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	for _, u := range s.users {
		go u.App.runAutomation(s.ctx, syncInterval)
	}
}

func (s *Server) session(r *http.Request) (*userAccount, string) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, ""
	}
	hash := fingerprint(cookie.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok {
		return nil, ""
	}
	if time.Now().After(session.Expires) {
		delete(s.sessions, hash)
		return nil, ""
	}
	return s.users[session.UserID], hash
}

func (s *Server) publicSession(w http.ResponseWriter, r *http.Request) {
	u, _ := s.session(r)
	var user any
	var inbox any
	if u != nil {
		s.mu.Lock()
		p := u.Profile
		s.mu.Unlock()
		d := u.App.store.snapshot()
		user = map[string]string{"id": fingerprint("google:" + p.Subject), "name": p.Name, "email": p.Email}
		connected := u.App.gmail.connected()
		inbox = map[string]any{"connected": connected, "email": d.GmailAccount, "choiceComplete": connected || d.OnboardingSkipped}
	}
	jsonOut(w, 200, map[string]any{"user": user, "inbox": inbox, "configured": s.root.gmail.configured(), "loginRedirectUri": loginRedirectURI, "gmailRedirectUri": redirectURI})
}

func flowCookie(kind string) string { return "ledger_" + kind + "_oauth" }
func flowRedirect(kind string) string {
	if kind == "login" {
		return loginRedirectURI
	}
	return redirectURI
}

func (s *Server) begin(w http.ResponseWriter, r *http.Request, kind string) {
	if !strings.HasPrefix(r.Host, "127.0.0.1:") {
		fail(w, 400, errors.New("open http://127.0.0.1:8080 to use Google sign-in"))
		return
	}
	u, session := s.session(r)
	account := ""
	if kind == "gmail" {
		if u == nil {
			fail(w, 401, errors.New("sign in before connecting an inbox"))
			return
		}
		var input struct {
			Account string `json:"account"`
		}
		if !decode(w, r, &input) {
			return
		}
		account = input.Account
		if account != "same" && account != "another" {
			fail(w, 400, errors.New("choose your sign-in email or another Gmail account"))
			return
		}
	}
	g := s.root.gmail
	g.mu.Lock()
	configured := g.configuredLocked()
	clientID := g.clientID
	g.mu.Unlock()
	if !configured {
		fail(w, 503, errors.New("Google sign-in is awaiting application setup by the owner"))
		return
	}
	state, verifier, nonce := randomID(), randomID(), randomID()
	flow := authFlow{Kind: kind, Verifier: verifier, Nonce: nonce, Expires: time.Now().Add(10 * time.Minute), ClientID: clientID}
	values := url.Values{"client_id": {clientID}, "redirect_uri": {flowRedirect(kind)}, "response_type": {"code"}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "scope": {"openid email profile"}, "prompt": {"select_account"}}
	digest := sha256.Sum256([]byte(verifier))
	values.Set("code_challenge", base64.RawURLEncoding.EncodeToString(digest[:]))
	s.mu.Lock()
	for key, f := range s.flows {
		if time.Now().After(f.Expires) {
			delete(s.flows, key)
		}
	}
	if len(s.flows) > 1000 {
		s.mu.Unlock()
		fail(w, 429, errors.New("too many pending sign-in attempts; try later"))
		return
	}
	if kind == "gmail" {
		flow.UserID = fingerprint("google:" + u.Profile.Subject)
		flow.Session = session
		values.Set("scope", "openid email "+gmailScope)
		values.Set("access_type", "offline")
		values.Set("prompt", "consent select_account")
		if account == "same" {
			flow.ExpectedEmail = u.Profile.Email
			values.Set("login_hint", u.Profile.Email)
			values.Set("prompt", "consent")
		}
	}
	s.flows[state] = flow
	s.mu.Unlock()
	path := "/api/auth/callback"
	if kind == "gmail" {
		path = "/api/gmail/callback"
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie(kind), Value: state, Path: path, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	jsonOut(w, 200, map[string]string{"url": "https://accounts.google.com/o/oauth2/v2/auth?" + values.Encode()})
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request, kind string) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(flowCookie(kind))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		s.authError(w, r, kind, "The Google sign-in session expired. Please try again.")
		return
	}
	s.mu.Lock()
	flow, ok := s.flows[state]
	delete(s.flows, state)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: flowCookie(kind), Path: r.URL.Path, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	if !ok || flow.Kind != kind || time.Now().After(flow.Expires) {
		s.authError(w, r, kind, "The Google sign-in session expired. Please try again.")
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.authError(w, r, kind, "Google access was not approved. You can try again or continue without connecting Gmail.")
		return
	}
	if kind == "gmail" {
		u, session := s.session(r)
		if u == nil || session != flow.Session {
			s.authError(w, r, kind, "Your app session changed. Sign in and connect Gmail again.")
			return
		}
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.authError(w, r, kind, "Google returned no authorization code. Try again.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	g := s.root.gmail
	g.mu.Lock()
	if g.clientID != flow.ClientID {
		g.mu.Unlock()
		s.authError(w, r, kind, "Application setup changed. Please start sign-in again.")
		return
	}
	token, err := g.requestToken(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {flowRedirect(kind)}, "code_verifier": {flow.Verifier}})
	client := g.client
	g.mu.Unlock()
	if err != nil {
		s.authError(w, r, kind, err.Error())
		return
	}
	identity, err := s.verifier.verify(ctx, client, token.IDToken, flow.ClientID, flow.Nonce)
	if err != nil {
		s.authError(w, r, kind, err.Error())
		return
	}
	if kind == "gmail" {
		if err = s.linkMailbox(ctx, r, flow, token, identity); err != nil {
			s.authError(w, r, kind, err.Error())
			return
		}
		http.Redirect(w, r, "/?gmail=connected", http.StatusSeeOther)
		return
	}
	// Identity uses Google's immutable subject, never the email address as a database key.
	sessionID := randomID()
	hash := fingerprint(sessionID)
	s.mu.Lock()
	u, err := s.openUserLocked(identity)
	if err == nil {
		if old, er := r.Cookie(sessionCookie); er == nil {
			delete(s.sessions, fingerprint(old.Value))
		}
		for key, v := range s.sessions {
			if time.Now().After(v.Expires) {
				delete(s.sessions, key)
			}
		}
		s.sessions[hash] = userSession{fingerprint("google:" + u.Profile.Subject), time.Now().Add(sessionLifetime)}
	}
	s.mu.Unlock()
	if err != nil {
		s.authError(w, r, kind, "Could not open your saved ledger. Your existing files were preserved.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sessionID, Path: "/", MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/?signed_in=1", http.StatusSeeOther)
}

func (s *Server) linkMailbox(ctx context.Context, r *http.Request, flow authFlow, token Token, identity Identity) error {
	hasScope := false
	for _, scope := range strings.Fields(token.Scope) {
		if scope == gmailScope {
			hasScope = true
		}
	}
	if !hasScope {
		return errors.New("Gmail access was not granted. Approve email access to connect your inbox")
	}
	if flow.ExpectedEmail != "" && !strings.EqualFold(flow.ExpectedEmail, identity.Email) {
		return errors.New("Google returned another email. Choose the same sign-in email, or select 'another account' in the app")
	}
	if token.RefreshToken == "" {
		return errors.New("Google did not provide offline access. Please reconnect and approve the consent screen")
	}
	// Do the network verification before taking the global users lock.
	s.root.gmail.mu.Lock()
	client := s.root.gmail.client
	s.root.gmail.mu.Unlock()
	probe := &Gmail{token: token, client: client}
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if err := probe.call(ctx, "GET", "profile", nil, &profile); err != nil {
		return err
	}
	if !strings.EqualFold(profile.Email, identity.Email) {
		return errors.New("Gmail account verification did not match; no connection was saved")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[flow.Session]
	if !ok || session.UserID != flow.UserID || time.Now().After(session.Expires) {
		return errors.New("your sign-in expired; sign in again before connecting Gmail")
	}
	u := s.users[flow.UserID]
	if u == nil {
		return errors.New("sign in again")
	}
	a := u.App
	if !a.syncMu.TryLock() {
		return errors.New("Gmail is syncing; try connecting again when it finishes")
	}
	defer a.syncMu.Unlock()
	d := a.store.snapshot()
	if d.GmailAccount != "" && !strings.EqualFold(d.GmailAccount, identity.Email) {
		return fmt.Errorf("this ledger is already bound to %s; reconnect that inbox to keep expenses separate", d.GmailAccount)
	}
	for id, other := range s.users {
		if id != flow.UserID && other.App.gmail.connected() && strings.EqualFold(other.App.store.snapshot().GmailAccount, identity.Email) {
			return errors.New("this inbox is already connected to another app profile; disconnect it there first")
		}
	}
	token.IDToken = ""
	// Bind the store first. A failed token write leaves a disconnected but safely bound ledger.
	if err := a.store.update(func(db *Database) error { db.GmailAccount = identity.Email; db.OnboardingSkipped = false; return nil }); err != nil {
		return err
	}
	a.gmail.mu.Lock()
	defer a.gmail.mu.Unlock()
	if err := atomicJSON(a.gmail.path, token); err != nil {
		return err
	}
	a.gmail.token = token
	a.queueGmail(true)
	return nil
}

func (s *Server) authError(w http.ResponseWriter, r *http.Request, kind, message string) {
	key := "auth_error"
	if kind == "gmail" {
		key = "gmail_error"
	}
	http.Redirect(w, r, "/?"+url.Values{key: {message}}.Encode(), http.StatusSeeOther)
}

func (s *Server) configure(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Ledger-Owner")), []byte(s.ownerKey)) != 1 {
		fail(w, 403, errors.New("the local developer key is required to change application configuration"))
		return
	}
	var input struct {
		Credentials json.RawMessage `json:"credentials"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wasConfigured := s.root.gmail.configured()
	for _, u := range s.users {
		if wasConfigured && u.App.gmail.connected() {
			fail(w, 409, errors.New("disconnect all inboxes before replacing application credentials"))
			return
		}
	}
	if err := s.root.gmail.configure(input.Credentials); err != nil {
		fail(w, 400, err)
		return
	}
	s.root.gmail.mu.Lock()
	defer s.root.gmail.mu.Unlock()
	for _, u := range s.users {
		u.App.gmail.mu.Lock()
		u.App.gmail.clientID = s.root.gmail.clientID
		u.App.gmail.secret = s.root.gmail.secret
		u.App.gmail.mu.Unlock()
	}
	s.flows = map[string]authFlow{}
	jsonOut(w, 200, map[string]bool{"configured": true})
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.publicSession)
	mux.HandleFunc("POST /api/auth/google", func(w http.ResponseWriter, r *http.Request) { s.begin(w, r, "login") })
	mux.HandleFunc("GET /api/auth/callback", func(w http.ResponseWriter, r *http.Request) { s.finish(w, r, "login") })
	mux.HandleFunc("GET /api/gmail/callback", func(w http.ResponseWriter, r *http.Request) { s.finish(w, r, "gmail") })
	mux.HandleFunc("POST /api/gmail/connect", func(w http.ResponseWriter, r *http.Request) { s.begin(w, r, "gmail") })
	mux.HandleFunc("POST /api/gmail/disconnect", func(w http.ResponseWriter, r *http.Request) {
		u, _ := s.session(r)
		if u == nil {
			fail(w, 401, errors.New("please sign in to manage your inbox"))
			return
		}
		s.mu.Lock()
		id := fingerprint("google:" + u.Profile.Subject)
		for state, f := range s.flows {
			if f.Kind == "gmail" && f.UserID == id {
				delete(s.flows, state)
			}
		}
		s.mu.Unlock()
		u.handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		_, session := s.session(r)
		s.mu.Lock()
		delete(s.sessions, session)
		for key, f := range s.flows {
			if f.Session == session {
				delete(s.flows, key)
			}
		}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		jsonOut(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/developer/google-config", s.configure)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		u, _ := s.session(r)
		if u == nil {
			fail(w, 401, errors.New("please sign in to open your ledger"))
			return
		}
		if r.URL.Path == "/api/onboarding/skip" && r.Method == "POST" {
			if err := u.App.store.update(func(d *Database) error { d.OnboardingSkipped = true; return nil }); err != nil {
				fail(w, 500, err)
				return
			}
			jsonOut(w, 200, map[string]bool{"ok": true})
			return
		}
		u.handler.ServeHTTP(w, r)
	})
	// The public shell includes login and local developer setup; ledger data remains behind sessions.
	mux.Handle("/", s.root.routes())
	return localOnly(mux)
}
