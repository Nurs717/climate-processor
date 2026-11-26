package rest

import (
	"net/http"
	"strconv"
	"time"

	"climate-processor/internal/auth"
	"climate-processor/internal/domain"

	"github.com/gin-gonic/gin"
)

// limits are the rate limits of the API for its two kinds of clients.
//
// An anonymous client has the demo limit, counted per IP address. A signed-in owner has the owner limit: it is
// higher and counted per user, so it does not depend on the network the owner is on. Who a client is becomes known
// only when its session is checked, and that is a database query which a flood of made-up tokens must not reach.
// So a request with a token first pays one token, per IP address, to the owner limit (admit), and pays its whole
// cost, per user, only after the session has proved valid (settle).
type limits struct {
	demo  Limiter // nil = anonymous clients are not limited
	owner Limiter // nil = every client has the demo limit
	now   func() time.Time
}

// admit is the first limit of a request: the one in front of the session check.
func (l limits) admit(cost int) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch {
		case l.owner != nil && hasToken(c.Request):
			charge(c, l.owner, l.now(), "ip:"+c.ClientIP(), 1)
		case l.demo != nil:
			charge(c, l.demo, l.now(), c.ClientIP(), cost)
		}
	}
}

// settle is the second limit of a request: the one after the session check. An anonymous client has paid in full
// by now; an owner pays the cost of the request to the owner limit.
func (l limits) settle(cost int) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := identityOf(c)
		if l.owner == nil || id == auth.Anonymous {
			return
		}
		switch {
		case id.Role == domain.RoleOwner:
			charge(c, l.owner, l.now(), "user:"+strconv.FormatInt(id.UserID, 10), cost)
		case l.demo != nil:
			// A role without a limit of its own gets the strictest one.
			charge(c, l.demo, l.now(), c.ClientIP(), cost)
		}
	}
}

// hasToken tells whether the request carries a session token, valid or not.
func hasToken(r *http.Request) bool {
	token, _ := tokenOf(r)
	return token != ""
}
