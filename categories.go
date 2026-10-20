package main

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

func categoryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return "", errors.New("label must be 1 to 40 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("label cannot contain control characters")
		}
	}
	return name, nil
}

func categoryExists(d Database, name string) bool {
	for _, category := range allCategories(d) {
		if strings.EqualFold(category, name) {
			return true
		}
	}
	return false
}

func customCategoryIndex(d Database, name string) int {
	for i, category := range d.CustomCategories {
		if category == name {
			return i
		}
	}
	return -1
}

func (a *App) createCategory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	name, err := categoryName(input.Name)
	if err != nil {
		fail(w, 400, err)
		return
	}
	err = a.store.update(func(d *Database) error {
		if categoryExists(*d, name) {
			return errors.New("that label already exists")
		}
		d.CustomCategories = append(d.CustomCategories, name)
		return nil
	})
	if err != nil {
		fail(w, 400, err)
		return
	}
	jsonOut(w, 201, map[string]string{"name": name})
}

func (a *App) renameCategory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if !decode(w, r, &input) {
		return
	}
	newName, err := categoryName(input.NewName)
	if err != nil {
		fail(w, 400, err)
		return
	}
	err = a.store.update(func(d *Database) error {
		index := customCategoryIndex(*d, input.Name)
		if index < 0 {
			return errors.New("only your own labels can be renamed")
		}
		if !strings.EqualFold(d.CustomCategories[index], newName) && categoryExists(*d, newName) {
			return errors.New("that label already exists")
		}
		oldName := d.CustomCategories[index]
		d.CustomCategories[index] = newName
		for i := range d.Transactions {
			if d.Transactions[i].Category == oldName {
				d.Transactions[i].Category = newName
			}
		}
		return nil
	})
	if err != nil {
		fail(w, 400, err)
		return
	}
	jsonOut(w, 200, map[string]string{"name": newName})
}

func (a *App) deleteCategory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Replacement string `json:"replacement"`
	}
	if !decode(w, r, &input) {
		return
	}
	err := a.store.update(func(d *Database) error {
		index := customCategoryIndex(*d, input.Name)
		if index < 0 {
			return errors.New("only your own labels can be removed")
		}
		used := false
		for _, transaction := range d.Transactions {
			if transaction.Category == input.Name {
				used = true
				break
			}
		}
		if used && (!validCategory(input.Replacement, *d) || input.Replacement == input.Name) {
			return errors.New("choose another label for existing expenses")
		}
		for i := range d.Transactions {
			if d.Transactions[i].Category == input.Name {
				d.Transactions[i].Category = input.Replacement
			}
		}
		d.CustomCategories = append(d.CustomCategories[:index], d.CustomCategories[index+1:]...)
		return nil
	})
	if err != nil {
		fail(w, 400, err)
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
