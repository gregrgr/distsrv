package server

import (
	"context"
	"net/http"
	"time"

	"distsrv/internal/auth"
	"distsrv/internal/db"
)

const sessionCookieName = "distsrv_session"

type ctxKey int

const (
	ctxUserKey ctxKey = iota
)

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := auth.RandomToken(32)
	if err != nil {
		return err
	}
	ttl := time.Duration(s.cfg.Security.SessionTTLHours) * time.Hour
	if err := s.db.CreateSession(token, userID, ttl); err != nil {
		return err
	}
	secure := !s.cfg.Server.DevMode
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(ttl),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) destroySession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.db.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) currentUser(r *http.Request) (*db.User, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	sess, err := s.db.GetSession(c.Value)
	if err != nil {
		return nil, false
	}
	u, err := s.db.GetUserByID(sess.UserID)
	if err != nil {
		return nil, false
	}
	return u, true
}

func userFromContext(ctx context.Context) (*db.User, bool) {
	u, ok := ctx.Value(ctxUserKey).(*db.User)
	return u, ok
}
