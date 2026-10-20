package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBankAlerts(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name, bank, body, merchant, date, account, ref, category string
		amount                                                   int64
		bad                                                      bool
	}{
		{name: "HDFC credit card", bank: "HDFC", body: "Rs. 1,234.50 spent on HDFC Bank Credit Card ending 1234 at AMAZON on 19-Oct-2026. Ref: ABC123456. Available limit INR 50,000.00", merchant: "AMAZON", date: "2026-10-19", account: "1234", ref: "ABC123456", category: "Shopping", amount: 123450},
		{name: "Axis UPI", bank: "Axis", body: "INR 549.00 debited from A/c XX5678 on 20-10-2026 to ZOMATO via UPI. UPI Ref No 629123456789. Avl bal INR 20,000.00", merchant: "ZOMATO", date: "2026-10-20", account: "5678", ref: "629123456789", category: "Food & drink", amount: 54900},
		{name: "HDFC UPI sent", bank: "HDFC", body: "Sent Rs.120.00 From HDFC Bank A/C XX4821 To SWIGGY On 05/09/26 Ref 528374912345 UPI:swiggy@icici Avl Bal Rs.18422.65", merchant: "SWIGGY", date: "2026-09-05", account: "4821", ref: "528374912345", category: "Food & drink", amount: 12000},
		{name: "Axis UPI txn", bank: "Axis", body: "Your UPI txn of INR 85.00 from A/c XX5678 to CAFE on 20-10-2026 was successful.", merchant: "CAFE", date: "2026-10-20", account: "5678", category: "Food & drink", amount: 8500},
		{name: "HDFC UPI transfer", bank: "HDFC", body: "UPI transfer of INR 350.00 from your account XX1234 to AMAZON on 20-10-2026.", merchant: "AMAZON", date: "2026-10-20", account: "1234", category: "Shopping", amount: 35000},
		{name: "HTML and rupee", bank: "HDFC", body: "<p>₹90.5 paid to Uber on 20/10/2026.</p><p>Card XX9876</p>", merchant: "Uber", date: "2026-10-20", account: "9876", category: "Transport", amount: 9050},
		{name: "IST date fallback", bank: "Axis", body: "INR 500 debited to Bigbasket.", merchant: "Bigbasket", date: "2026-10-21", category: "Groceries", amount: 50000},
		{name: "Indian grouping", bank: "Axis", body: "INR 1,23,456.78 spent at IKEA on 2026-10-20.", merchant: "IKEA", date: "2026-10-20", category: "Shopping", amount: 12345678},
		{name: "debit with safety footer", bank: "HDFC", body: "INR 200.00 spent at AMAZON on 20-10-2026. Do not share your OTP. Contact us if a payment failed.", merchant: "AMAZON", date: "2026-10-20", category: "Shopping", amount: 20000},
		{name: "credit", bank: "HDFC", body: "INR 500 credited to account 1234", bad: true},
		{name: "refund", bank: "Axis", body: "Refund INR 500 for amount debited", bad: true},
		{name: "UPI received", bank: "Axis", body: "UPI payment of INR 500 received in your account XX1234", bad: true},
		{name: "UPI collect request", bank: "HDFC", body: "UPI collect request for payment of INR 500 from your account", bad: true},
		{name: "reversed debit", bank: "Axis", body: "INR 500 debited from account XX1234 and reversed immediately", bad: true},
		{name: "OTP", bank: "HDFC", body: "OTP for transaction of INR 500", bad: true},
		{name: "failed", bank: "Axis", body: "Your payment of INR 500 failed", bad: true},
		{name: "statement", bank: "HDFC", body: "Amount due INR 500 for purchases", bad: true},
		{name: "ambiguous", bank: "Axis", body: "INR 500 paid to A and INR 100 paid to B", bad: true},
		{name: "unsupported", bank: "Other", body: "INR 500 debited", bad: true},
		{name: "decimal precision", bank: "Axis", body: "INR 123.456 debited", bad: true},
		{name: "invalid grouping", bank: "Axis", body: "INR 1,2,3 debited", bad: true},
		{name: "bad date", bank: "Axis", body: "INR 500 debited on 32/10/2026", bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseEmail(c.bank, "Transaction alert", c.body, now)
			if c.bad {
				if err == nil {
					t.Fatalf("expected rejection, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Amount != c.amount || got.Merchant != c.merchant || got.Date != c.date || got.Account != c.account || got.Reference != c.ref || got.Category != c.category {
				t.Fatalf("unexpected extraction: %+v", got)
			}
		})
	}
}
func newTestApp(t *testing.T) *App {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &App{store: s, gmail: &Gmail{client: &http.Client{}}}
}
func sample() Transaction {
	return Transaction{Bank: "HDFC", Account: "1234", Amount: 50000, Date: "2026-10-20", Merchant: "Amazon", Category: "Shopping", Reference: "REF123456"}
}
func TestDuplicatePersistenceAndReview(t *testing.T) {
	a := newTestApp(t)
	var id string
	err := a.store.update(func(d *Database) error {
		id = addTransaction(d, sample(), "msg1")
		addTransaction(d, sample(), "msg1")
		addTransaction(d, sample(), "msg2")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d := a.store.snapshot()
	if len(d.Transactions) != 1 || len(d.Transactions[0].SourceIDs) != 2 {
		t.Fatal("duplicate alert was not merged")
	}
	if err = a.store.update(func(d *Database) error {
		d.Transactions[0].Amount = 40000
		d.Transactions[0].Status = "accepted"
		setLabels(d, d.Transactions[0])
		addTransaction(d, sample(), "msg3")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openStore(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	d = reloaded.snapshot()
	if len(d.Transactions) != 1 || d.Transactions[0].ID != id || d.Messages["msg3"].Label != "Expense Ledger/Accepted" || d.Messages["msg2"].Label != "Expense Ledger/Accepted" {
		t.Fatal("review edits broke deduplication or labels")
	}
	if err = a.store.update(func(d *Database) error {
		x := sample()
		x.Reference = ""
		addTransaction(d, x, "msg4")
		addTransaction(d, x, "msg5")
		x.Reference = "DIFFERENT123"
		addTransaction(d, x, "msg6")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d = a.store.snapshot()
	if len(d.Transactions) != 4 || d.Transactions[2].DuplicateOf == "" {
		t.Fatal("uncertain duplicates must remain reviewable")
	}
}
func TestFailedCommitKeepsMemory(t *testing.T) {
	a := newTestApp(t)
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	a.store.path = filepath.Join(block, "ledger.json")
	err := a.store.update(func(d *Database) error { addTransaction(d, sample(), "msg1"); return nil })
	if err == nil || len(a.store.snapshot().Transactions) != 0 {
		t.Fatal("failed write changed ledger")
	}
}
func TestCorruptStoreIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	_ = os.WriteFile(path, []byte("invalid json"), 0600)
	if _, err := openStore(path); err == nil {
		t.Fatal("corrupt data silently accepted")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "invalid json" {
		t.Fatal("corrupt ledger overwritten")
	}
}
func request(a *App, method, path, body, origin, host, header string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Ledger-Request", header)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func TestLocalRequestGuards(t *testing.T) {
	a := newTestApp(t)
	for _, c := range []struct {
		host, origin, header string
		want                 int
	}{{"127.0.0.1:8080", "http://evil.example", "1", 403}, {"evil.example", "", "1", 403}, {"127.0.0.1:8080", "", "", 403}, {"127.0.0.1:8080", "http://127.0.0.1:8080", "1", 201}, {"localhost:8080", "http://localhost:5173", "1", 201}} {
		b, _ := json.Marshal(sample())
		w := request(a, "POST", "/api/transactions", string(b), c.origin, c.host, c.header)
		if w.Code != c.want {
			t.Fatalf("guard %+v: %d %s", c, w.Code, w.Body.String())
		}
	}
}

func TestAssetPathCannotEscapeFrontend(t *testing.T) {
	a := newTestApp(t)
	w := request(a, "GET", "/..%5c..%5cgo.mod", "", "", "127.0.0.1:8080", "")
	if strings.Contains(w.Body.String(), "module expense-ledger") {
		t.Fatal("asset request exposed source outside frontend")
	}
}
func TestPossibleDuplicateNeedsConfirmation(t *testing.T) {
	a := newTestApp(t)
	var id string
	_ = a.store.update(func(d *Database) error {
		x := sample()
		x.Reference = ""
		addTransaction(d, x, "one")
		id = addTransaction(d, x, "two")
		return nil
	})
	input := map[string]any{"merchant": "Amazon", "category": "Shopping", "date": "2026-10-20", "amount": 50000, "note": "", "status": "accepted", "allowDuplicate": false}
	b, _ := json.Marshal(input)
	w := request(a, "PATCH", "/api/transactions/"+id, string(b), "", "127.0.0.1:8080", "1")
	if w.Code != 400 {
		t.Fatal("unconfirmed duplicate accepted")
	}
	input["allowDuplicate"] = true
	b, _ = json.Marshal(input)
	w = request(a, "PATCH", "/api/transactions/"+id, string(b), "", "127.0.0.1:8080", "1")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestPasteIdempotencyAndCSV(t *testing.T) {
	a := newTestApp(t)
	body := `{"bank":"HDFC","subject":"alert","body":"INR 100 debited to Amazon on 20-10-2026."}`
	for i := 0; i < 2; i++ {
		w := request(a, "POST", "/api/parse", body, "", "127.0.0.1:8080", "1")
		if w.Code != 201 {
			t.Fatal(w.Body.String())
		}
	}
	if len(a.store.snapshot().Transactions) != 1 {
		t.Fatal("pasted alert duplicated")
	}
	for _, s := range []string{"=HYPERLINK(1)", " +cmd", "\t@SUM(A1)", "-1"} {
		if !strings.HasPrefix(csvSafe(s), "'") {
			t.Fatalf("unsafe CSV cell %q", s)
		}
	}
}
func TestSenderAndMIME(t *testing.T) {
	for _, c := range []struct{ from, want string }{{"HDFC <alerts@hdfcbank.net>", "HDFC"}, {"alerts@notify.axisbank.com", "Axis"}, {"HDFC <alerts@hdfcbank.net.evil.example>", ""}, {"HDFC <alerts@evilhdfcbank.net>", ""}, {"Axis Bank <attacker@example.com>", ""}} {
		if got := bankFromSender(c.from); got != c.want {
			t.Fatalf("bad sender %q: %q", c.from, got)
		}
	}
	var p MIMEPart
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"mimeType":"multipart/alternative","parts":[{"mimeType":"text/html","body":{"data":%q}},{"mimeType":"text/plain","body":{"data":%q}}]}`, base64.RawURLEncoding.EncodeToString([]byte("<p>INR 100</p>")), base64.RawURLEncoding.EncodeToString([]byte("INR 100")))), &p)
	if got := bodyText(p); got != "INR 100" {
		t.Fatalf("MIME alternatives duplicated: %s", got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestLabelFailureRetriesAfterSave(t *testing.T) {
	a := newTestApp(t)
	_ = a.store.update(func(d *Database) error {
		d.GmailAccount = "owner@example.com"
		addTransaction(d, sample(), "gmail-id")
		return nil
	})
	a.gmail.token = Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}
	fail := true
	modified := 0
	a.gmail.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			return response(200, `{"emailAddress":"owner@example.com"}`), nil
		case strings.HasSuffix(r.URL.Path, "/labels"):
			return response(200, `{"labels":[{"id":"L1","name":"Expense Ledger/Review"},{"id":"L2","name":"Expense Ledger/Accepted"},{"id":"L3","name":"Expense Ledger/Ignored"}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/modify"):
			modified++
			if fail {
				return response(429, `{}`), nil
			}
			return response(200, `{}`), nil
		}
		t.Fatalf("unexpected Gmail request %s", r.URL.Path)
		return nil, nil
	})}
	if err := a.flushLabels(context.Background()); err == nil {
		t.Fatal("failed label reported success")
	}
	if a.store.snapshot().Messages["gmail-id"].Applied != "" {
		t.Fatal("failed label marked applied")
	}
	fail = false
	if err := a.flushLabels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if modified != 2 || a.store.snapshot().Messages["gmail-id"].Applied != "Expense Ledger/Review" {
		t.Fatal("label not retried")
	}
}
func TestWrongGmailAccountCannotApplyLabels(t *testing.T) {
	a := newTestApp(t)
	_ = a.store.update(func(d *Database) error {
		d.GmailAccount = "owner@example.com"
		addTransaction(d, sample(), "gmail-id")
		return nil
	})
	a.gmail.token = Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}
	a.gmail.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/profile") {
			t.Fatal("wrong account modified")
		}
		return response(200, `{"emailAddress":"other@example.com"}`), nil
	})}
	if a.flushLabels(context.Background()) == nil {
		t.Fatal("wrong account allowed")
	}
}

func TestGmailSyncPaginationAndIdempotency(t *testing.T) {
	a := newTestApp(t)
	a.gmail.token = Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}
	fetched := 0
	modified := 0
	a.gmail.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/")
		switch path {
		case "profile":
			return response(200, `{"emailAddress":"owner@example.com"}`), nil
		case "messages":
			if r.URL.Query().Get("pageToken") == "page2" {
				return response(200, `{"messages":[{"id":"second"},{"id":"credit"}]}`), nil
			}
			return response(200, `{"messages":[{"id":"first"}],"nextPageToken":"page2"}`), nil
		case "labels":
			return response(200, `{"labels":[{"id":"L1","name":"Expense Ledger/Review"},{"id":"L2","name":"Expense Ledger/Accepted"},{"id":"L3","name":"Expense Ledger/Ignored"}]}`), nil
		case "messages/first/modify", "messages/second/modify":
			modified++
			return response(200, `{}`), nil
		case "messages/first", "messages/second", "messages/credit":
			fetched++
			text := "INR 500 debited to Amazon on 20-10-2026. Account XX1234. Ref REF123456."
			if path == "messages/credit" {
				text = "INR 500 credited to Account XX1234."
			}
			m := map[string]any{"internalDate": "1792454400000", "payload": map[string]any{"mimeType": "text/plain", "headers": []map[string]string{{"name": "From", "value": "alerts@hdfcbank.net"}, {"name": "Subject", "value": "Bank alert"}}, "body": map[string]string{"data": base64.RawURLEncoding.EncodeToString([]byte(text))}}}
			b, _ := json.Marshal(m)
			return response(200, string(b)), nil
		}
		t.Fatalf("unexpected Gmail request %s", r.URL)
		return nil, nil
	})}
	result, err := a.syncGmail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Duplicates != 1 || result.Skipped != 1 || result.Scanned != 3 || result.LabelError != "" {
		t.Fatalf("unexpected sync result %+v", result)
	}
	if len(a.store.snapshot().Transactions) != 1 || modified != 2 {
		t.Fatal("sync did not merge and label alerts")
	}
	result, err = a.syncGmail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 0 || result.Duplicates != 0 || modified != 2 || fetched != 4 {
		t.Fatalf("sync repeated imported message or label work: %+v, fetch %d, modify %d", result, fetched, modified)
	}
}
