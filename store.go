package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var categories = []string{"Food & drink", "Groceries", "Shopping", "Transport", "Bills & utilities", "Entertainment", "Health", "Travel", "Transfers", "Other"}

type Transaction struct {
	ID          string   `json:"id"`
	Bank        string   `json:"bank"`
	Account     string   `json:"account"`
	Amount      int64    `json:"amount"` // integer paise
	Date        string   `json:"date"`
	Merchant    string   `json:"merchant"`
	Reference   string   `json:"reference"`
	Category    string   `json:"category"`
	Status      string   `json:"status"`
	Note        string   `json:"note"`
	DuplicateOf string   `json:"duplicateOf,omitempty"`
	SourceIDs   []string `json:"sourceIds"`
	Subject     string   `json:"subject"`
	Excerpt     string   `json:"excerpt"`
	CreatedAt   string   `json:"createdAt"`
	OriginalKey string   `json:"originalKey,omitempty"`
}
type MessageRecord struct {
	TransactionID string `json:"transactionId"`
	Label         string `json:"label"`
	Applied       string `json:"applied"`
}
type Database struct {
	Transactions      []Transaction            `json:"transactions"`
	Messages          map[string]MessageRecord `json:"messages"`
	CustomCategories  []string                 `json:"customCategories,omitempty"`
	LastSync          string                   `json:"lastSync"`
	LastScan          SyncResult               `json:"lastScan"`
	GmailAccount      string                   `json:"gmailAccount"`
	OnboardingSkipped bool                     `json:"onboardingSkipped"`
}

func allCategories(d Database) []string {
	result := make([]string, 0, len(categories)+len(d.CustomCategories))
	result = append(result, categories...)
	return append(result, d.CustomCategories...)
}

type Store struct {
	mu   sync.Mutex
	path string
	db   Database
}

func openStore(path string) (*Store, error) {
	s := &Store{path: path, db: Database{Transactions: []Transaction{}, Messages: map[string]MessageRecord{}}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.db); err != nil {
		return nil, fmt.Errorf("cannot read ledger (file preserved): %w", err)
	}
	if s.db.Messages == nil {
		s.db.Messages = map[string]MessageRecord{}
	}
	if s.db.Transactions == nil {
		s.db.Transactions = []Transaction{}
	}
	return s, nil
}
func atomicJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ledger-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// Commit a copy so a failed write never changes the in-memory ledger.
func (s *Store) update(fn func(*Database) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.db)
	var next Database
	_ = json.Unmarshal(b, &next)
	if err := fn(&next); err != nil {
		return err
	}
	if err := atomicJSON(s.path, &next); err != nil {
		return err
	}
	s.db = next
	return nil
}
func (s *Store) snapshot() Database {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.db)
	var d Database
	_ = json.Unmarshal(b, &d)
	return d
}
func labelFor(t Transaction) string {
	switch t.Status {
	case "accepted":
		return "Expense Ledger/Accepted"
	case "rejected":
		return "Expense Ledger/Ignored"
	default:
		return "Expense Ledger/Review"
	}
}
func setLabels(d *Database, t Transaction) {
	for _, id := range t.SourceIDs {
		if r, ok := d.Messages[id]; ok {
			r.Label = labelFor(t)
			d.Messages[id] = r
		}
	}
}
func addTransaction(d *Database, t Transaction, source string) string {
	if r, ok := d.Messages[source]; ok {
		return r.TransactionID
	}
	t.OriginalKey = fingerprint(t.Bank + "|" + t.Account + "|" + t.Reference + "|" + fmt.Sprint(t.Amount))
	for i := range d.Transactions {
		old := &d.Transactions[i]
		if t.Reference != "" && (old.OriginalKey == t.OriginalKey || (old.OriginalKey == "" && old.Reference == t.Reference && old.Bank == t.Bank && old.Account == t.Account && old.Amount == t.Amount)) {
			old.SourceIDs = append(old.SourceIDs, source)
			d.Messages[source] = MessageRecord{TransactionID: old.ID, Label: labelFor(*old)}
			return old.ID
		}
		if old.Status != "rejected" && old.Bank == t.Bank && old.Account == t.Account && old.Date == t.Date && old.Amount == t.Amount && normalize(old.Merchant) == normalize(t.Merchant) && (old.Reference == "" || t.Reference == "") {
			t.DuplicateOf = old.ID
		}
	}
	t.ID = randomID()
	t.Status = "pending"
	t.SourceIDs = []string{source}
	t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	d.Transactions = append(d.Transactions, t)
	d.Messages[source] = MessageRecord{TransactionID: t.ID, Label: labelFor(t)}
	return t.ID
}
