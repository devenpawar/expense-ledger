package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const loginRedirectURI = "http://127.0.0.1:8080/api/auth/callback"
const gmailScope = "https://www.googleapis.com/auth/gmail.modify"

type Identity struct {
	Subject string `json:"subject"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

type IDVerifier struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

func (v *IDVerifier) key(ctx context.Context, client *http.Client, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Now().Before(v.expires) {
		if k := v.keys[kid]; k != nil {
			return k, nil
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v3/certs", nil)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not verify Google sign-in; try again")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Google sign-in verification is unavailable")
	}
	var jwks struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string } `json:"keys"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("invalid Google signing keys")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
			continue
		}
		n, e1 := base64.RawURLEncoding.DecodeString(k.N)
		e, e2 := base64.RawURLEncoding.DecodeString(k.E)
		if e1 != nil || e2 != nil || len(e) > 4 || len(n) < 256 {
			continue
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent*256 + int(b)
		}
		if exponent < 3 || exponent%2 == 0 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid Google signing keys")
	}
	v.keys = keys
	v.expires = time.Now().Add(time.Hour)
	if k := keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("unknown Google signing key; try signing in again")
}

// Only server-exchanged ID tokens are accepted; none are accepted from browser JSON.
func (v *IDVerifier) verify(ctx context.Context, client *http.Client, raw, clientID, nonce string) (Identity, error) {
	invalid := fmt.Errorf("Google sign-in could not be verified; please try again")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || len(raw) > 32000 {
		return Identity{}, invalid
	}
	head, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, invalid
	}
	var h struct{ Alg, Kid string }
	if json.Unmarshal(head, &h) != nil || h.Alg != "RS256" || h.Kid == "" {
		return Identity{}, invalid
	}
	key, err := v.key(ctx, client, h.Kid)
	if err != nil {
		return Identity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, invalid
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return Identity{}, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, invalid
	}
	var c struct {
		Issuer    string          `json:"iss"`
		Audience  json.RawMessage `json:"aud"`
		Presenter string          `json:"azp"`
		Subject   string          `json:"sub"`
		Email     string          `json:"email"`
		Verified  json.RawMessage `json:"email_verified"`
		Name      string          `json:"name"`
		Nonce     string          `json:"nonce"`
		Expiry    int64           `json:"exp"`
		Issued    int64           `json:"iat"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return Identity{}, invalid
	}
	var audiences []string
	var audience string
	if json.Unmarshal(c.Audience, &audience) == nil {
		audiences = []string{audience}
	} else if json.Unmarshal(c.Audience, &audiences) != nil {
		return Identity{}, invalid
	}
	audienceOK := false
	for _, a := range audiences {
		if a == clientID {
			audienceOK = true
		}
	}
	verified := string(c.Verified) == "true" || string(c.Verified) == `"true"`
	now := time.Now().Unix()
	if !audienceOK || (len(audiences) > 1 && c.Presenter != clientID) || (c.Presenter != "" && c.Presenter != clientID) || (c.Issuer != "https://accounts.google.com" && c.Issuer != "accounts.google.com") || c.Expiry <= now || c.Issued <= 0 || c.Issued > now+60 || c.Expiry <= c.Issued || c.Subject == "" || len(c.Subject) > 255 || !verified || !strings.Contains(c.Email, "@") || len(c.Email) > 320 || nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return Identity{}, invalid
	}
	if c.Name == "" {
		c.Name = strings.Split(c.Email, "@")[0]
	}
	if len(c.Name) > 200 {
		c.Name = c.Name[:200]
	}
	return Identity{c.Subject, c.Email, c.Name}, nil
}
