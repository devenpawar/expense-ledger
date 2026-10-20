package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const redirectURI = "http://127.0.0.1:8080/api/gmail/callback"

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	Expiry       time.Time `json:"expiry"`
	IDToken      string    `json:"id_token,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}
type Gmail struct {
	mu                     sync.Mutex
	clientID, secret, path string
	token                  Token
	client                 *http.Client
	configPath             string
	configFromEnvironment  bool
}

func newGmail(dir string) (*Gmail, error) {
	g := &Gmail{clientID: os.Getenv("GOOGLE_CLIENT_ID"), secret: os.Getenv("GOOGLE_CLIENT_SECRET"), path: filepath.Join(dir, "gmail-token.json"), client: &http.Client{Timeout: 30 * time.Second}}
	if err := g.loadConfiguration(dir); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(g.path)
	if err == nil {
		err = json.Unmarshal(b, &g.token)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read Gmail token: %w", err)
	}
	return g, nil
}
func (g *Gmail) configured() bool       { g.mu.Lock(); defer g.mu.Unlock(); return g.configuredLocked() }
func (g *Gmail) configuredLocked() bool { return g.clientID != "" && g.secret != "" }
func (g *Gmail) connected() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.token.RefreshToken != "" || g.token.AccessToken != ""
}
func (g *Gmail) fetchToken(ctx context.Context, v url.Values) error {
	t, err := g.requestToken(ctx, v)
	if err != nil {
		return err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = g.token.RefreshToken
	}
	t.IDToken = ""
	if err = atomicJSON(g.path, &t); err != nil {
		return err
	}
	g.token = t
	return nil
}

// Caller holds g.mu. The returned token is not stored until its intended account is verified.
func (g *Gmail) requestToken(ctx context.Context, v url.Values) (Token, error) {
	v.Set("client_id", g.clientID)
	v.Set("client_secret", g.secret)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(v.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.client.Do(req)
	if err != nil {
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return Token{}, fmt.Errorf("The server cannot reach Google because its network connection was denied. Start the app with start.cmd from Windows Explorer, then try again")
		}
		return Token{}, fmt.Errorf("The server could not reach Google to finish sign-in. Check your internet connection and try again")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Token{}, fmt.Errorf("Google authorization failed (%d); try signing in or connecting again", res.StatusCode)
	}
	var t Token
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t); err != nil {
		return Token{}, err
	}
	if t.AccessToken == "" {
		return Token{}, fmt.Errorf("Google returned no access token")
	}
	t.Expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return t, nil
}
func (g *Gmail) access(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token.AccessToken != "" && time.Now().Before(g.token.Expiry.Add(-time.Minute)) {
		return g.token.AccessToken, nil
	}
	if !g.configuredLocked() || g.token.RefreshToken == "" {
		return "", fmt.Errorf("connect Gmail first")
	}
	if err := g.fetchToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.token.RefreshToken}}); err != nil {
		return "", err
	}
	return g.token.AccessToken, nil
}
func (g *Gmail) call(ctx context.Context, method, path string, body any, out any) error {
	token, err := g.access(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://gmail.googleapis.com/gmail/v1/users/me/"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("Gmail request failed; check your connection")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Gmail returned HTTP %d; reconnect for authorization errors or retry later", res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
	}
	return nil
}

type MIMEPart struct {
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []MIMEPart `json:"parts"`
}

func bodyText(p MIMEPart) string {
	if p.MimeType == "text/plain" || p.MimeType == "text/html" {
		b, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.Body.Data, "="))
		if e == nil {
			return string(b)
		}
	}
	// Prefer plain text in multipart/alternative so we do not count amounts twice.
	for _, child := range p.Parts {
		if child.MimeType == "text/plain" {
			if s := bodyText(child); s != "" {
				return s
			}
		}
	}
	var parts []string
	for _, child := range p.Parts {
		if s := bodyText(child); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}
func header(p MIMEPart, name string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
func bankFromSender(from string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return ""
	}
	i := strings.LastIndex(addr.Address, "@")
	if i < 0 {
		return ""
	}
	domain := strings.ToLower(addr.Address[i+1:])
	for _, r := range []struct{ domain, bank string }{{"hdfcbank.net", "HDFC"}, {"hdfcbank.com", "HDFC"}, {"axisbank.com", "Axis"}, {"axis.bank.in", "Axis"}, {"hdfc.bank.in", "HDFC"}} {
		if domain == r.domain || strings.HasSuffix(domain, "."+r.domain) {
			return r.bank
		}
	}
	return ""
}

type SyncResult struct {
	Imported    int            `json:"imported"`
	Duplicates  int            `json:"duplicates"`
	Skipped     int            `json:"skipped"`
	Scanned     int            `json:"scanned"`
	Known       int            `json:"known"`
	Limited     bool           `json:"limited"`
	LabelError  string         `json:"labelError,omitempty"`
	SkipReasons map[string]int `json:"skipReasons,omitempty"`
}

func (a *App) syncGmail(ctx context.Context) (SyncResult, error) {
	var result SyncResult
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if err := a.gmail.call(ctx, "GET", "profile", nil, &profile); err != nil {
		return result, err
	}
	if profile.Email == "" {
		return result, fmt.Errorf("Gmail returned no account identity")
	}
	if err := a.store.update(func(d *Database) error {
		if d.GmailAccount != "" && !strings.EqualFold(d.GmailAccount, profile.Email) {
			return fmt.Errorf("this ledger belongs to %s; reconnect that Gmail account", d.GmailAccount)
		}
		d.GmailAccount = profile.Email
		return nil
	}); err != nil {
		return result, err
	}
	page := ""
	for {
		q := url.Values{"q": {"{from:hdfcbank.net from:hdfcbank.com from:axisbank.com from:axis.bank.in from:hdfc.bank.in} -in:spam -in:trash"}, "maxResults": {"100"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		var list struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			Next string `json:"nextPageToken"`
		}
		if err := a.gmail.call(ctx, "GET", "messages?"+q.Encode(), nil, &list); err != nil {
			return result, err
		}
		for _, item := range list.Messages {
			result.Scanned++
			d := a.store.snapshot()
			if _, ok := d.Messages[item.ID]; ok {
				result.Known++
				continue
			}
			var m struct {
				ID           string   `json:"id"`
				InternalDate string   `json:"internalDate"`
				Payload      MIMEPart `json:"payload"`
			}
			if err := a.gmail.call(ctx, "GET", "messages/"+url.PathEscape(item.ID)+"?format=full", nil, &m); err != nil {
				return result, err
			}
			bank := bankFromSender(header(m.Payload, "From"))
			ms, err := strconv.ParseInt(m.InternalDate, 10, 64)
			if err != nil {
				return result, fmt.Errorf("invalid Gmail received timestamp")
			}
			t, parseErr := parseEmail(bank, header(m.Payload, "Subject"), bodyText(m.Payload), time.UnixMilli(ms))
			if parseErr != nil {
				result.Skipped++
				if result.SkipReasons == nil {
					result.SkipReasons = make(map[string]int)
				}
				result.SkipReasons[parseErr.Error()]++
				continue
			}
			err = a.store.update(func(d *Database) error {
				before := len(d.Transactions)
				addTransaction(d, t, item.ID)
				if len(d.Transactions) > before {
					result.Imported++
				} else {
					result.Duplicates++
				}
				return nil
			})
			if err != nil {
				return result, err
			}
		}
		page = list.Next
		if page == "" {
			break
		}
		if result.Scanned >= 1000 {
			result.Limited = true
			break
		}
	}
	if err := a.store.update(func(d *Database) error {
		d.LastSync = time.Now().UTC().Format(time.RFC3339)
		d.LastScan = result
		return nil
	}); err != nil {
		return result, err
	}
	if err := a.flushLabels(ctx); err != nil {
		result.LabelError = err.Error()
	}
	return result, nil
}
func (a *App) flushLabels(ctx context.Context) error {
	d := a.store.snapshot()
	pending := false
	for id, r := range d.Messages {
		if !strings.HasPrefix(id, "local:") && r.Label != r.Applied {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if err := a.gmail.call(ctx, "GET", "profile", nil, &profile); err != nil {
		return err
	}
	if d.GmailAccount == "" || !strings.EqualFold(d.GmailAccount, profile.Email) {
		return fmt.Errorf("reconnect the Gmail account associated with this ledger before applying labels")
	}
	var labels struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := a.gmail.call(ctx, "GET", "labels", nil, &labels); err != nil {
		return err
	}
	names := []string{"Expense Ledger/Review", "Expense Ledger/Accepted", "Expense Ledger/Ignored"}
	ids := map[string]string{}
	for _, l := range labels.Labels {
		ids[l.Name] = l.ID
	}
	for _, name := range names {
		if ids[name] == "" {
			var l struct {
				ID string `json:"id"`
			}
			if err := a.gmail.call(ctx, "POST", "labels", map[string]string{"name": name, "labelListVisibility": "labelShow", "messageListVisibility": "show"}, &l); err != nil {
				return err
			}
			ids[name] = l.ID
		}
	}
	for id, r := range d.Messages {
		if r.Label == r.Applied || strings.HasPrefix(id, "local:") {
			continue
		}
		remove := []string{}
		for _, name := range names {
			if name != r.Label {
				remove = append(remove, ids[name])
			}
		}
		if err := a.gmail.call(ctx, "POST", "messages/"+url.PathEscape(id)+"/modify", map[string]any{"addLabelIds": []string{ids[r.Label]}, "removeLabelIds": remove}, nil); err != nil {
			return err
		}
		if err := a.store.update(func(db *Database) error {
			current := db.Messages[id]
			current.Applied = r.Label
			db.Messages[id] = current
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
