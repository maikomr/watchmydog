package ports

import (
	"context"
	"errors"
	"time"
)

// Principal is an authenticated caller.
type Principal struct {
	// Subject identifies the caller. The single-user deployment uses
	// "owner"; a multi-tenant deployment would carry user and tenant IDs.
	Subject string
}

// Session is an issued login session.
type Session struct {
	// Token is opaque to callers; the HTTP layer stores it in a cookie.
	Token     string
	Principal Principal
	ExpiresAt time.Time
}

// Authenticator verifies credentials and session tokens.
//
// The HTTP layer owns transport concerns (cookies, rate limiting); the
// implementation owns what a credential and a token are. Verify is the
// stable part: a future OIDC adapter would issue sessions from an identity
// provider callback instead of Login, but verify them the same way.
type Authenticator interface {
	// Login exchanges a secret for a session.
	Login(ctx context.Context, secret string) (Session, error)
	// Verify checks a session token and returns who it belongs to.
	Verify(ctx context.Context, token string) (Principal, error)
}

var (
	// ErrInvalidCredentials is returned by Login for a wrong secret.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidSession is returned by Verify for a bad or expired token.
	ErrInvalidSession = errors.New("invalid or expired session")
)
