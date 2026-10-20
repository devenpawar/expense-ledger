package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	store      *Store
	gmail      *Gmail
	syncMu     sync.Mutex
	runtimeMu  sync.Mutex
	runtime    SyncStatus
	jobs       chan bool
	syncCancel context.CancelFunc
}

func main() {
	dir := os.Getenv("LEDGER_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	s, err := openStore(filepath.Join(dir, "ledger.json"))
	if err != nil {
		log.Fatal(err)
	}
	g, err := newGmail(dir)
	if err != nil {
		log.Fatal(err)
	}
	a := &App{store: s, gmail: g, jobs: make(chan bool, 32)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	application, err := newServer(ctx, dir, a)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: application.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 60 * time.Second}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatal(err)
	}
	application.start()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("Expense Ledger: http://127.0.0.1:8080 (local only)")
	log.Print("Developer setup: http://127.0.0.1:8080/developer (owner key is in the data directory: developer-key)")
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, err error) {
	jsonOut(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, fmt.Errorf("invalid request: %w", err))
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, errors.New("request must contain one JSON object"))
		return false
	}
	return true
}
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("POST /api/transactions", a.create)
	mux.HandleFunc("PATCH /api/transactions/{id}", a.review)
	mux.HandleFunc("POST /api/categories", a.createCategory)
	mux.HandleFunc("PATCH /api/categories", a.renameCategory)
	mux.HandleFunc("DELETE /api/categories", a.deleteCategory)
	mux.HandleFunc("POST /api/parse", a.parse)
	mux.HandleFunc("POST /api/gmail/sync", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.performSync(r.Context())
		if err != nil {
			jsonOut(w, 502, map[string]any{"error": err.Error(), "partial": result})
			return
		}
		jsonOut(w, 200, result)
	})
	mux.HandleFunc("POST /api/gmail/labels", func(w http.ResponseWriter, r *http.Request) {
		if !a.syncMu.TryLock() {
			fail(w, 409, errors.New("Gmail is busy"))
			return
		}
		defer a.syncMu.Unlock()
		if err := a.flushLabels(r.Context()); err != nil {
			fail(w, 502, err)
			return
		}
		jsonOut(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/gmail/disconnect", func(w http.ResponseWriter, r *http.Request) {
		a.runtimeMu.Lock()
		cancel := a.syncCancel
		a.runtimeMu.Unlock()
		if cancel != nil {
			cancel()
		}
		a.syncMu.Lock()
		defer a.syncMu.Unlock()
		a.gmail.mu.Lock()
		defer a.gmail.mu.Unlock()
		if err := os.Remove(a.gmail.path); err != nil && !os.IsNotExist(err) {
			fail(w, 500, err)
			return
		}
		a.gmail.token = Token{}
		a.runtimeMu.Lock()
		a.runtime = SyncStatus{}
		a.runtimeMu.Unlock()
		jsonOut(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/export", a.export)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, errors.New("API route not found")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", 405)
			return
		}
		root := "frontend/dist"
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		rootAbs, rootErr := filepath.Abs(root)
		fileAbs, fileErr := filepath.Abs(path)
		rel, relErr := filepath.Rel(rootAbs, fileAbs)
		if rootErr != nil || fileErr != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			http.Error(w, "Invalid asset path", 400)
			return
		}
		if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
			http.Error(w, "Frontend not built. Run npm install and npm run build in frontend, then reload.", 503)
			return
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			http.ServeFile(w, r, path)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
	return localOnly(mux)
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "Local host required", 403)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, e := url.Parse(origin)
				if e != nil || (u.Host != "127.0.0.1:8080" && u.Host != "localhost:8080" && u.Host != "127.0.0.1:5173" && u.Host != "localhost:5173") || u.Scheme != "http" {
					http.Error(w, "Origin not allowed", 403)
					return
				}
			}
			if r.Header.Get("X-Ledger-Request") != "1" {
				http.Error(w, "Missing local request header", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) state(w http.ResponseWriter, r *http.Request) {
	d := a.store.snapshot()
	sort.SliceStable(d.Transactions, func(i, j int) bool { return d.Transactions[i].Date > d.Transactions[j].Date })
	pending := 0
	for id, m := range d.Messages {
		if !strings.HasPrefix(id, "local:") && m.Label != m.Applied {
			pending++
		}
	}
	jsonOut(w, 200, map[string]any{"transactions": d.Transactions, "categories": allCategories(d), "customCategories": append([]string{}, d.CustomCategories...), "lastSync": d.LastSync, "lastScan": d.LastScan, "labelsPending": pending, "sync": a.syncStatus(), "gmail": map[string]any{"configured": a.gmail.configured(), "connected": a.gmail.connected(), "redirectUri": redirectURI, "email": d.GmailAccount}})
}
func validCategory(s string, d Database) bool {
	for _, c := range allCategories(d) {
		if c == s {
			return true
		}
	}
	return false
}
func validate(t Transaction, d Database) error {
	if t.Bank != "HDFC" && t.Bank != "Axis" {
		return errors.New("choose HDFC or Axis")
	}
	if t.Amount <= 0 || t.Amount > 100000000000 {
		return errors.New("amount must be between ₹0.01 and ₹1,000,000,000")
	}
	if _, err := time.Parse("2006-01-02", t.Date); err != nil {
		return errors.New("date must be YYYY-MM-DD")
	}
	if strings.TrimSpace(t.Merchant) == "" || len(t.Merchant) > 160 {
		return errors.New("merchant is required (up to 160 characters)")
	}
	if !validCategory(t.Category, d) {
		return errors.New("choose a valid category")
	}
	if len(t.Account) > 4 || strings.Trim(t.Account, "0123456789") != "" {
		return errors.New("account must contain at most four digits")
	}
	if len(t.Note) > 1000 {
		return errors.New("note is too long")
	}
	return nil
}
func (a *App) create(w http.ResponseWriter, r *http.Request) {
	var t Transaction
	if !decode(w, r, &t) {
		return
	}
	t.Reference = strings.ToUpper(strings.TrimSpace(t.Reference))
	if len(t.Reference) > 100 {
		fail(w, 400, errors.New("reference is too long"))
		return
	}
	t.Subject = "Manual entry"
	t.Excerpt = ""
	t.DuplicateOf = ""
	var id string
	var invalid error
	err := a.store.update(func(d *Database) error {
		if invalid = validate(t, *d); invalid != nil {
			return invalid
		}
		id = addTransaction(d, t, "local:"+randomID())
		return nil
	})
	if err != nil {
		if invalid != nil {
			fail(w, 400, err)
		} else {
			fail(w, 500, err)
		}
		return
	}
	jsonOut(w, 201, map[string]string{"id": id})
}
func (a *App) parse(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Bank    string `json:"bank"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if !decode(w, r, &input) {
		return
	}
	t, err := parseEmail(input.Bank, input.Subject, input.Body, time.Now())
	if err != nil {
		fail(w, 422, err)
		return
	}
	// A deterministic source fingerprint makes repeated pasted alerts idempotent.
	source := "local:" + fingerprint(input.Bank+"|"+plain(input.Body))
	var id string
	err = a.store.update(func(d *Database) error { id = addTransaction(d, t, source); return nil })
	if err != nil {
		fail(w, 500, err)
		return
	}
	jsonOut(w, 201, map[string]string{"id": id})
}
func (a *App) review(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Merchant       string `json:"merchant"`
		Category       string `json:"category"`
		Date           string `json:"date"`
		Amount         int64  `json:"amount"`
		Note           string `json:"note"`
		Status         string `json:"status"`
		AllowDuplicate bool   `json:"allowDuplicate"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Status != "pending" && input.Status != "accepted" && input.Status != "rejected" {
		fail(w, 400, errors.New("invalid review status"))
		return
	}
	err := a.store.update(func(d *Database) error {
		for i := range d.Transactions {
			t := &d.Transactions[i]
			if t.ID != r.PathValue("id") {
				continue
			}
			t.Merchant = strings.TrimSpace(input.Merchant)
			t.Category = input.Category
			t.Date = input.Date
			t.Amount = input.Amount
			t.Note = input.Note
			if err := validate(*t, *d); err != nil {
				return err
			}
			if input.Status == "accepted" && t.DuplicateOf != "" && !input.AllowDuplicate {
				return errors.New("possible duplicate: confirm it is a separate expense before accepting")
			}
			t.Status = input.Status
			setLabels(d, *t)
			return nil
		}
		return os.ErrNotExist
	})
	if err != nil {
		code := 400
		if errors.Is(err, os.ErrNotExist) {
			code = 404
		}
		fail(w, code, err)
		return
	}
	a.queueGmail(false)
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func csvSafe(s string) string {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + s
	}
	return s
}
func (a *App) export(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=expense-ledger.csv")
	out := csv.NewWriter(w)
	defer out.Flush()
	_ = out.Write([]string{"Date", "Bank", "Account", "Merchant", "Amount (INR)", "Category", "Status", "Reference", "Note"})
	for _, t := range a.store.snapshot().Transactions {
		_ = out.Write([]string{t.Date, t.Bank, t.Account, csvSafe(t.Merchant), strconv.FormatInt(t.Amount/100, 10) + fmt.Sprintf(".%02d", t.Amount%100), t.Category, t.Status, csvSafe(t.Reference), csvSafe(t.Note)})
	}
}
