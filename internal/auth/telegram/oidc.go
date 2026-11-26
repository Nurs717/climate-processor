package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The endpoints of "Log In With Telegram" (https://core.telegram.org/bots/telegram-login). They are fixed, so
// the service does not need the network to start: the signing keys are fetched on the first sign-in.
const (
	issuer        = "https://oauth.telegram.org"
	authEndpoint  = issuer + "/auth"
	tokenEndpoint = issuer + "/token"
	jwksURL       = issuer + "/.well-known/jwks.json"

	httpTimeout = 10 * time.Second
	maxNameLen  = 100 // runes of the display name that are kept
)

// oidcProvider is the Provider that talks to Telegram, on top of go-oidc and x/oauth2.
type oidcProvider struct {
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

// NewProvider returns the Provider of "Log In With Telegram". clientID and clientSecret are shown by
// @BotFather (Login Widget, OpenID Connect), redirectURL is the callback address registered there. ctx bounds
// the life of the key set that is fetched from Telegram.
func NewProvider(ctx context.Context, clientID, clientSecret, redirectURL string) (Provider, error) {
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		return nil, errors.New("telegram: client id, client secret and redirect URL are required")
	}
	client := &http.Client{Timeout: httpTimeout} // the default client has no timeout
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), jwksURL)
	return &oidcProvider{
		config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   authEndpoint,
				TokenURL:  tokenEndpoint,
				AuthStyle: oauth2.AuthStyleInHeader, // Telegram wants the credentials in a Basic header
			},
			RedirectURL: redirectURL,
			Scopes:      []string{oidc.ScopeOpenID, "profile"}, // "profile" brings the Telegram user id
		},
		// RS256 is the default of @BotFather; pinning it makes a changed setting fail loudly.
		verifier: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256}}),
		client:   client,
	}, nil
}

func (p *oidcProvider) AuthURL(state, nonce, codeVerifier string) string {
	return p.config.AuthCodeURL(state,
		oauth2.S256ChallengeOption(codeVerifier),
		oauth2.SetAuthURLParam("nonce", nonce))
}

func (p *oidcProvider) Exchange(ctx context.Context, code, codeVerifier string) (IDToken, error) {
	ctx = oidc.ClientContext(ctx, p.client)
	token, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return IDToken{}, fmt.Errorf("exchange the code: %w", err)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return IDToken{}, errors.New("the token response has no id_token")
	}
	verified, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return IDToken{}, fmt.Errorf("verify the ID token: %w", err)
	}

	var claims struct {
		ID       json.Number `json:"id"` // a number in Telegram's examples; a numeric string is accepted as well
		Name     string      `json:"name"`
		Username string      `json:"preferred_username"`
	}
	if err := verified.Claims(&claims); err != nil {
		return IDToken{}, fmt.Errorf("read the ID token claims: %w", err)
	}
	user, err := userFromClaims(claims.ID.String(), claims.Name, claims.Username)
	if err != nil {
		return IDToken{}, err
	}
	return IDToken{Nonce: verified.Nonce, User: user}, nil
}

// userFromClaims turns the claims of a verified ID token into a User. Without a valid Telegram user id there
// is nobody to sign in.
func userFromClaims(id, name, username string) (User, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return User{}, fmt.Errorf("the ID token has no valid Telegram user id (%q)", id)
	}
	return User{ID: n, Name: cleanText(name), Username: cleanText(username)}, nil
}

// cleanText trims a display value chosen by the user and keeps it within a sane size and valid UTF-8.
func cleanText(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) > maxNameLen {
		s = string([]rune(s)[:maxNameLen])
	}
	return s
}
