package main

import (
	"encoding/json"
	"testing"
)

func TestPersonalLabelsFollowExpenses(t *testing.T) {
	a := newTestApp(t)
	call := func(method, body string) int {
		return request(a, method, "/api/categories", body, "", "127.0.0.1:8080", "1").Code
	}
	if code := call("POST", `{"name":"  Subscriptions  "}`); code != 201 {
		t.Fatalf("create label: %d", code)
	}
	if code := call("POST", `{"name":"subscriptions"}`); code != 400 {
		t.Fatalf("case-insensitive duplicate: %d", code)
	}
	if code := call("PATCH", `{"name":"Food & drink","newName":"Dining"}`); code != 400 {
		t.Fatalf("renamed built-in label: %d", code)
	}
	transaction := sample()
	transaction.Category = "Subscriptions"
	body, _ := json.Marshal(transaction)
	if w := request(a, "POST", "/api/transactions", string(body), "", "127.0.0.1:8080", "1"); w.Code != 201 {
		t.Fatalf("custom label rejected: %d %s", w.Code, w.Body.String())
	}
	if code := call("PATCH", `{"name":"Subscriptions","newName":"Recurring"}`); code != 200 {
		t.Fatalf("rename label: %d", code)
	}
	if d := a.store.snapshot(); len(d.CustomCategories) != 1 || d.CustomCategories[0] != "Recurring" || d.Transactions[0].Category != "Recurring" {
		t.Fatal("renaming did not update existing expense")
	}
	if code := call("DELETE", `{"name":"Recurring"}`); code != 400 {
		t.Fatalf("removed used label without reassignment: %d", code)
	}
	if code := call("DELETE", `{"name":"Recurring","replacement":"Other"}`); code != 200 {
		t.Fatalf("remove and reassign: %d", code)
	}
	d := a.store.snapshot()
	if len(d.CustomCategories) != 0 || d.Transactions[0].Category != "Other" {
		t.Fatal("removing label did not preserve expense")
	}
	reloaded, err := openStore(a.store.path)
	if err != nil || reloaded.snapshot().Transactions[0].Category != "Other" {
		t.Fatal("label changes did not persist")
	}
}
