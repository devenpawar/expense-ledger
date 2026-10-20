package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type GoogleCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func parseGoogleCredentials(raw json.RawMessage) (GoogleCredentials, error) {
	var downloaded struct {
		Web *struct {
			ClientID     string   `json:"client_id"`
			ClientSecret string   `json:"client_secret"`
			RedirectURIs []string `json:"redirect_uris"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &downloaded); err != nil || downloaded.Web == nil {
		return GoogleCredentials{}, fmt.Errorf("choose the JSON downloaded for a Google Web application OAuth client")
	}
	c := GoogleCredentials{strings.TrimSpace(downloaded.Web.ClientID), strings.TrimSpace(downloaded.Web.ClientSecret)}
	if !strings.HasSuffix(c.ClientID, ".apps.googleusercontent.com") || len(c.ClientID) > 300 || c.ClientSecret == "" || len(c.ClientSecret) > 300 {
		return GoogleCredentials{}, fmt.Errorf("the file does not contain valid Google OAuth client credentials")
	}
	found := false
	loginFound := false
	for _, uri := range downloaded.Web.RedirectURIs {
		if uri == redirectURI {
			found = true
		}
		if uri == loginRedirectURI {
			loginFound = true
		}
	}
	if !found || !loginFound {
		return GoogleCredentials{}, fmt.Errorf("add both %s and %s to the OAuth client's authorized redirect URIs, then download its JSON again", loginRedirectURI, redirectURI)
	}
	return c, nil
}

func (g *Gmail) configure(raw json.RawMessage) error {
	c, err := parseGoogleCredentials(raw)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token.AccessToken != "" || g.token.RefreshToken != "" {
		return fmt.Errorf("disconnect Gmail before replacing the application's OAuth credentials")
	}
	if g.configFromEnvironment {
		return fmt.Errorf("this app uses OAuth credentials from its environment; update those and restart it")
	}
	if err = atomicJSON(g.configPath, c); err != nil {
		return fmt.Errorf("could not save the application's OAuth configuration")
	}
	g.clientID = c.ClientID
	g.secret = c.ClientSecret
	return nil
}

func (g *Gmail) loadConfiguration(dir string) error {
	g.configPath = filepath.Join(dir, "google-oauth.json")
	if g.clientID != "" || g.secret != "" {
		if g.clientID == "" || g.secret == "" {
			return fmt.Errorf("set both GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET, or unset both to use developer setup")
		}
		g.configFromEnvironment = true
		return nil
	}
	b, err := os.ReadFile(g.configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read application OAuth configuration")
	}
	var c GoogleCredentials
	if err = json.Unmarshal(b, &c); err != nil || c.ClientID == "" || c.ClientSecret == "" {
		return fmt.Errorf("invalid application OAuth configuration; file preserved")
	}
	g.clientID = c.ClientID
	g.secret = c.ClientSecret
	return nil
}
