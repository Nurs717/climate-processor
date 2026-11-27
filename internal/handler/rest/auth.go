package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/auth"

	"github.com/gin-gonic/gin"
)

const (
	// TelegramCallbackPath is where Telegram sends the browser back after the sign-in. The whole address
	// (the public URL plus this path) is registered as a Redirect URI in @BotFather.
	TelegramCallbackPath = "/api/v1/auth/telegram/callback"

	sessionCookie  = "climate_session" // the session token of the owner
	flowCookie     = "climate_signin"  // the state of a sign-in in progress, ties it to this browser
	flowCookiePath = "/api/v1/auth/telegram"

	identityKey = "identity" // key in the gin context

	// A sign-in calls Telegram for the token and, the first time, for the signing keys, each call with its own
	// 10 s timeout. This bound keeps the whole request inside the 15 s write timeout of the server, so that a
	// slow Telegram gives a clean error instead of a session the browser never receives.
	signInTimeout = 12 * time.Second

	// A sign-in that did not work sends the browser to the home page with the reason after this path, and the web
	// app says what happened. Without it, the person who came back from Telegram would be left on a page of JSON.
	signInFailedPath = "/?signin="
)

// The reasons that follow signInFailedPath. The web app (static/account.js) has a sentence for each.
const (
	reasonCancelled   = "cancelled"   // the person pressed Cancel in Telegram
	reasonNotOwner    = "not_owner"   // a Telegram account that is not the owner's
	reasonExpired     = "expired"     // the sign-in is too old, was used already, or was started in another browser
	reasonBusy        = "busy"        // too many sign-ins are in progress
	reasonUnavailable = "unavailable" // the session store is down
	reasonFailed      = "failed"      // Telegram did not confirm the sign-in, or anything else went wrong
)

// me: GET /api/v1/auth/me tells which role the client has.
func (a *api) me(c *gin.Context) error {
	c.JSON(http.StatusOK, newMe(identityOf(c), a.signIn != nil))
	return nil
}

// telegramLogin: GET /api/v1/auth/telegram/login sends the browser to Telegram to confirm the sign-in. The state
// of the sign-in goes to the same browser in a cookie; the callback accepts only a browser that has it.
func (a *api) telegramLogin(c *gin.Context) error {
	redirectURL, state, err := a.signIn.Begin()
	if err != nil {
		return a.signInFailed(c, reasonFor(err), err)
	}
	a.setCookie(c, flowCookie, flowCookiePath, state, int(a.signIn.FlowTTL()/time.Second))
	c.Redirect(http.StatusFound, redirectURL)
	return nil
}

// telegramCallback: GET /api/v1/auth/telegram/callback?code=&state= is where Telegram sends the browser back.
// For the owner it starts a session and goes to the home page. Anybody else, and any sign-in that fails, goes to the
// home page too, with the reason in the address (see signInFailed).
func (a *api) telegramCallback(c *gin.Context) error {
	const op = "rest.telegramCallback"
	// The address holds the one-time code: the page it redirects to must not get it in a Referer header.
	c.Header("Referrer-Policy", "no-referrer")
	browserState := cookieValue(c.Request, flowCookie)
	// A sign-in is tried once, with whatever result: the state is single use on the server anyway.
	a.clearCookie(c, flowCookie, flowCookiePath)

	q := c.Request.URL.Query()
	if refusal := q.Get("error"); refusal != "" { // the user pressed Cancel, or Telegram refused
		cancelled := apperr.Wrap(fmt.Errorf("telegram returned error %.64q", refusal), apperr.CodeUnauthorized, op, "Telegram sign-in was cancelled")
		return a.signInFailed(c, reasonCancelled, cancelled)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), signInTimeout)
	defer cancel()
	user, err := a.signIn.Finish(ctx, q.Get("state"), browserState, q.Get("code"))
	if err != nil {
		return a.signInFailed(c, reasonFor(err), err)
	}
	token, session, err := a.sessions.Create(ctx, user.ID, c.Request.UserAgent())
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeForbidden {
			a.log.WarnContext(ctx, "sign-in refused: the account is not an owner", "telegram_id", user.ID, "username", user.Username)
		}
		return a.signInFailed(c, reasonFor(err), err)
	}
	a.setCookie(c, sessionCookie, "/", token, int(session.ExpiresAt.Sub(a.now())/time.Second))
	c.Redirect(http.StatusFound, "/")
	return nil
}

// signInFailed sends the browser back to the home page with the reason in the address. The cause goes to the access
// log, as for any failed request. The 302 hides a failure of ours from the status-based level of that log, so it is
// logged at ERROR here (the request id ties the two records together).
func (a *api) signInFailed(c *gin.Context, reason string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err // the browser has left: nobody is there to redirect
	}
	_ = c.Error(err)
	if statusOf(apperr.CodeOf(err)) >= http.StatusInternalServerError {
		a.log.ErrorContext(c.Request.Context(), "sign-in failed", "reason", reason)
	}
	c.Redirect(http.StatusFound, signInFailedPath+reason)
	return nil
}

// reasonFor is the reason the web app is told for a failure.
func reasonFor(err error) string {
	switch apperr.CodeOf(err) {
	case apperr.CodeForbidden:
		return reasonNotOwner
	case apperr.CodeBadRequest:
		return reasonExpired
	case apperr.CodeTooManyRequests:
		return reasonBusy
	case apperr.CodeUnavailable:
		return reasonUnavailable
	default:
		return reasonFailed
	}
}

// logout: POST /api/v1/auth/logout ends the session of the request and removes its cookie. Signing out twice,
// or without a session, is fine. A token is revoked on the server, so a copy of it stops working too.
func (a *api) logout(c *gin.Context) error {
	if token, _ := tokenOf(c.Request); token != "" {
		if err := a.sessions.Revoke(c.Request.Context(), token); err != nil {
			return err // the cookie stays: the session is still alive, and the user can try again
		}
	}
	a.clearCookie(c, sessionCookie, "/")
	c.Status(http.StatusNoContent)
	return nil
}

// identify finds out who makes the request and stores the answer for identityOf. A request without a token is
// anonymous (the demo role). A token that is not valid is refused with 401, and its cookie is removed so that
// the browser stops sending it. A session store that is down gives 503 and keeps the cookie: the session may
// well be fine.
func (a *api) identify(c *gin.Context) {
	if a.sessions == nil {
		return
	}
	token, fromCookie := tokenOf(c.Request)
	if token == "" {
		return
	}
	id, err := a.sessions.Authenticate(c.Request.Context(), token)
	if err != nil {
		if fromCookie && apperr.CodeOf(err) == apperr.CodeUnauthorized {
			a.clearCookie(c, sessionCookie, "/")
		}
		renderError(c, err)
		return
	}
	c.Set(identityKey, id)
}

// identityOf is who makes the request; Anonymous when identify found no token.
func identityOf(c *gin.Context) auth.Identity {
	if id, ok := c.Get(identityKey); ok {
		if identity, ok := id.(auth.Identity); ok {
			return identity
		}
	}
	return auth.Anonymous
}

// tokenOf returns the session token of a request and whether it came from the cookie. A Bearer token (an app)
// wins over the cookie (a browser). An Authorization header of another kind is not ours and is ignored.
func tokenOf(r *http.Request) (token string, fromCookie bool) {
	if scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		if value = strings.TrimSpace(value); value != "" {
			return value, false
		}
	}
	return cookieValue(r, sessionCookie), true
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// setCookie sends a cookie the page script cannot read (HttpOnly). SameSite=Lax keeps the browser from sending it
// with requests started by other sites, but still sends it when the user follows a link or Telegram redirects
// back to the callback.
func (a *api) setCookie(c *gin.Context, name, path, value string, maxAgeSeconds int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCookie tells the browser to forget a cookie.
func (a *api) clearCookie(c *gin.Context, name, path string) {
	a.setCookie(c, name, path, "", -1) // a negative MaxAge becomes "Max-Age=0": delete now
}
