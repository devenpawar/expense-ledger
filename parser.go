package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func fingerprint(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func normalize(s string) string   { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

var tags = regexp.MustCompile(`(?s)<[^>]*>`)
var scripts = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(?:script|style)>`)
var spaces = regexp.MustCompile(`\s+`)

func plain(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(tags.ReplaceAllString(scripts.ReplaceAllString(s, " "), " ")), " "))
}

var amountRE = regexp.MustCompile(`(?i)(?:INR|Rs\.?|₹)\s*([0-9][0-9,]*(?:\.[0-9]+)?)`)
var numberRE = regexp.MustCompile(`^(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+|[0-9]{1,2}(?:,[0-9]{2})*,[0-9]{3})(?:\.[0-9]{1,2})?$`)
var debitRE = regexp.MustCompile(`(?i)\b(debited|spent|paid|purchased|purchase|payment of|transaction of|txn of|used (?:for|at))\b`)
var upiOutgoingRE = regexp.MustCompile(`(?i)\b(sent|transferred|transfer of|payment successful)\b`)
var creditRE = regexp.MustCompile(`(?i)\b(credited|received|refund(?:ed)?|reversed|declined|failed|unsuccessful|collect request|otp|one.time password|payment due|amount due|minimum due|statement generated)\b`)
var reversalRE = regexp.MustCompile(`(?i)\b(credited back|received|refund(?:ed)?|reversed|declined|failed|unsuccessful|collect request)\b`)
var accountRE = regexp.MustCompile(`(?i)(?:card|a/?c|account)(?:\s+(?:no\.?|number|ending|ending in|xx))?[\s:*xX.-]*([0-9]{4})\b`)
var refRE = regexp.MustCompile(`(?i)(?:UPI\s*(?:ref(?:erence)?|txn)|ref(?:erence)?(?:\s*(?:no|number))?|txn(?:\s*id)?|transaction\s*id|rrn)[\s:#.\-]*([a-zA-Z0-9]{6,})`)
var merchantRE = regexp.MustCompile(`(?i)\b(?:at|to|towards)\s+(.+?)(?:\s+(?:on|using|via|from|ref|UPI|Avl|available|not you|if you|for)\b|[.;]|$)`)
var dateRE = regexp.MustCompile(`\b(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{1,2}[-/ ][A-Za-z]{3,9}[-/ ][0-9]{2,4}|[0-9]{1,2}[-/][0-9]{1,2}[-/][0-9]{2,4})\b`)

func parseAmount(s string) (int64, error) {
	if !numberRE.MatchString(s) {
		return 0, fmt.Errorf("invalid currency amount")
	}
	s = strings.ReplaceAll(s, ",", "")
	parts := strings.Split(s, ".")
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n > 1000000000 || n < 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	p := int64(0)
	if len(parts) > 1 {
		v := parts[1]
		if len(v) == 1 {
			v += "0"
		}
		if len(v) != 2 {
			return 0, fmt.Errorf("use up to two decimal places")
		}
		p, err = strconv.ParseInt(v, 10, 64)
		if err != nil || p < 0 || p > 99 {
			return 0, fmt.Errorf("invalid paise")
		}
	}
	if len(parts) > 2 {
		return 0, fmt.Errorf("invalid amount")
	}
	return n*100 + p, nil
}
func categorize(merchant string) string {
	s := strings.ToLower(merchant)
	rules := []struct {
		category string
		words    []string
	}{
		{"Food & drink", []string{"swiggy", "zomato", "restaurant", "cafe", "coffee", "domino", "starbucks"}},
		{"Groceries", []string{"bigbasket", "blinkit", "zepto", "grocer", "dmart", "supermarket"}},
		{"Transport", []string{"uber", "ola", "metro", "petrol", "fuel", "rapido"}},
		{"Bills & utilities", []string{"electric", "airtel", "jio", "broadband", "water bill", "bescom"}},
		{"Entertainment", []string{"netflix", "spotify", "hotstar", "cinema", "bookmyshow"}},
		{"Health", []string{"pharmacy", "hospital", "apollo", "clinic", "medical"}},
		{"Travel", []string{"irctc", "indigo", "air india", "hotel", "makemytrip"}},
		{"Shopping", []string{"amazon", "flipkart", "myntra", "ikea"}},
	}
	for _, r := range rules {
		for _, w := range r.words {
			if strings.Contains(s, w) {
				return r.category
			}
		}
	}
	return "Other"
}

// Conservative extraction: ambiguous currency amounts and non-debits are left out.
func parseEmail(bank, subject, body string, received time.Time) (Transaction, error) {
	text := plain(body)
	combined := plain(subject) + " " + text
	if bank != "HDFC" && bank != "Axis" {
		return Transaction{}, fmt.Errorf("unsupported bank")
	}
	debit := debitRE.FindStringIndex(combined)
	if debit == nil && strings.Contains(strings.ToLower(combined), "upi") {
		debit = upiOutgoingRE.FindStringIndex(combined)
	}
	if debit == nil || creditRE.MatchString(combined[:debit[0]]) {
		return Transaction{}, fmt.Errorf("not a supported successful debit alert")
	}
	// A bank's safety footer can mention OTPs, refunds, or failed payments.
	// Reject reversals close to the debit statement, not unrelated footer text.
	statement := combined[debit[1]:]
	if end := strings.Index(statement, ". "); end >= 0 {
		statement = statement[:end]
	}
	if len(statement) > 180 {
		statement = statement[:180]
	}
	if reversalRE.MatchString(statement) {
		return Transaction{}, fmt.Errorf("not a supported successful debit alert")
	}
	matches := amountRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return Transaction{}, fmt.Errorf("no INR amount found")
	}
	// Only parse the transaction clause before balance / limit information.
	clause := regexp.MustCompile(`(?i)\b(?:avl|available|balance|credit limit)\b`).Split(text, 2)[0]
	amounts := amountRE.FindAllStringSubmatch(clause, -1)
	if len(amounts) != 1 {
		return Transaction{}, fmt.Errorf("ambiguous amount; use manual entry")
	}
	amount, err := parseAmount(amounts[0][1])
	if err != nil || amount <= 0 {
		return Transaction{}, fmt.Errorf("invalid debit amount")
	}
	t := Transaction{Bank: bank, Amount: amount, Date: received.In(time.FixedZone("IST", 19800)).Format("2006-01-02"), Merchant: "Unknown merchant", Category: "Other", Subject: plain(subject), Excerpt: text}
	if len(t.Excerpt) > 1600 {
		t.Excerpt = t.Excerpt[:1600]
	}
	if m := accountRE.FindStringSubmatch(text); len(m) > 1 {
		t.Account = m[1]
	}
	if m := refRE.FindStringSubmatch(text); len(m) > 1 {
		t.Reference = strings.ToUpper(m[1])
	}
	if m := merchantRE.FindStringSubmatch(clause); len(m) > 1 {
		t.Merchant = strings.TrimSpace(m[1])
	}
	if m := dateRE.FindString(text); m != "" {
		found := false
		for _, layout := range []string{"2006-01-02", "02-01-2006", "2-1-2006", "02/01/2006", "2/1/2006", "02-01-06", "2/1/06", "02-Jan-2006", "2-Jan-2006", "02-Jan-06", "2-Jan-06", "02 Jan 2006", "2 Jan 2006", "02/Jan/2006", "2 January 2006"} {
			if d, e := time.Parse(layout, m); e == nil {
				t.Date = d.Format("2006-01-02")
				found = true
				break
			}
		}
		if !found {
			return Transaction{}, fmt.Errorf("unrecognized transaction date; use manual entry")
		}
	}
	t.Category = categorize(t.Merchant)
	return t, nil
}
