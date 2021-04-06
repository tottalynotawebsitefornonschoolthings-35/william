package sessions

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// SessionStore is an interface to storing user sessions in the proxy
type SessionStore interface {
	Save(rw http.ResponseWriter, req *http.Request, s *SessionState) error
	Load(req *http.Request) (*SessionState, error)
	Clear(rw http.ResponseWriter, req *http.Request) error
}

var ErrLockNotObtained = errors.New("lock: not obtained")
var ErrNotLocked = errors.New("tried to release not existing lock")

type Lock interface {
	Obtain(ctx context.Context, expiration time.Duration) error
	Peek(ctx context.Context) (bool, error)
	Refresh(ctx context.Context, expiration time.Duration) error
	Release(ctx context.Context) error
}
