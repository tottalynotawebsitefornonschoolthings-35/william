package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/justinas/alice"
	middlewareapi "github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/middleware"
	sessionsapi "github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/sessions"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/logger"
)

// StoredSessionLoaderOptions cotnains all of the requirements to construct
// a stored session loader.
// All options must be provided.
type StoredSessionLoaderOptions struct {
	// Session storage basckend
	SessionStore sessionsapi.SessionStore

	// How often should sessions be refreshed
	RefreshPeriod time.Duration

	// Provider based session refreshing
	RefreshSession func(context.Context, *sessionsapi.SessionState) error

	// Provider based is session refresh need check
	IsRefreshSessionNeeded func(*sessionsapi.SessionState) bool

	// Provider based session validation.
	// If the sesssion is older than `RefreshPeriod` but the provider doesn't
	// refresh it, we must re-validate using this validation.
	ValidateSessionState func(context.Context, *sessionsapi.SessionState) bool
}

// NewStoredSessionLoader creates a new storedSessionLoader which loads
// sessions from the session store.
// If no session is found, the request will be passed to the nex handler.
// If a session was loader by a previous handler, it will not be replaced.
func NewStoredSessionLoader(opts *StoredSessionLoaderOptions) alice.Constructor {
	ss := &storedSessionLoader{
		store:                              opts.SessionStore,
		refreshPeriod:                      opts.RefreshPeriod,
		refreshSessionWithProvider:         opts.RefreshSession,
		isRefreshSessionNeededWithProvider: opts.IsRefreshSessionNeeded,
		validateSessionState:               opts.ValidateSessionState,
	}
	return ss.loadSession
}

// storedSessionLoader is responsible for loading sessions from cookie
// identified sessions in the session store.
type storedSessionLoader struct {
	store                              sessionsapi.SessionStore
	refreshPeriod                      time.Duration
	refreshSessionWithProvider         func(context.Context, *sessionsapi.SessionState) error
	isRefreshSessionNeededWithProvider func(*sessionsapi.SessionState) bool
	validateSessionState               func(context.Context, *sessionsapi.SessionState) bool
}

// loadSession attemptValidSession to load a session as identified by the request cookies.
// If no session is found, the request will be passed to the next handler.
// If a session was loader by a previous handler, it will not be replaced.
func (s *storedSessionLoader) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		scope := middlewareapi.GetRequestScope(req)
		// If scope is nil, this will panic.
		// A scope should always be injected before this handler is called.
		if scope.Session != nil {
			// The session was already loaded, pass to the next handler
			next.ServeHTTP(rw, req)
			return
		}

		session, err := s.getValidatedSession(rw, req)
		if err != nil {
			// In the case when there was an error loading the session,
			// we should clear the session
			logger.Errorf("Error loading cookied session: %v, removing session", err)
			err = s.store.Clear(rw, req)
			if err != nil {
				logger.Errorf("Error removing session: %v", err)
			}
		}

		// Add the session to the scope if it was found
		scope.Session = session
		next.ServeHTTP(rw, req)
	})
}

// getValidatedSession is responsible for loading a session and making sure
// that is is valid.
func (s *storedSessionLoader) getValidatedSession(rw http.ResponseWriter, req *http.Request) (*sessionsapi.SessionState, error) {
	session, err := s.store.Load(req)
	if err != nil {
		return nil, err
	}
	if session == nil {
		// No session was found in the storage, nothing more to do
		return nil, nil
	}

	return s.ensureSessionIsValid(rw, req, session)
}

func (s *storedSessionLoader) ensureSessionIsValid(rw http.ResponseWriter, req *http.Request, session *sessionsapi.SessionState) (*sessionsapi.SessionState, error) {
	if !s.isRefreshPeriodOver(session) {
		// Refresh is disabled or the session is not old enough, do nothing
		return session, nil
	}

	if s.isRefreshSessionNeededWithProvider(session) {
		return s.getRefreshedSession(rw, req)
	}

	// Session wasn't refreshed, so make sure it's still valid
	err := s.validateSession(req.Context(), session)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (s *storedSessionLoader) getRefreshedSession(rw http.ResponseWriter, req *http.Request) (*sessionsapi.SessionState, error) {
	session, err := s.store.LoadWithLock(req)
	defer s.store.ReleaseLock(req)
	if err != nil && err == sessionsapi.ErrNotLockable {
		// not able to lock session state which means other instance is currently refreshing session
		return s.retryLoadingValidSession(req, 10)
	}
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	if s.isRefreshSessionNeededWithProvider(session) {
		logger.Printf("Refreshing %s old session cookie for %s (refresh after %s)", session.Age(), session, s.refreshPeriod)
		err = s.refreshSession(rw, req, session)
		if err != nil {
			return nil, fmt.Errorf("error refreshing access token for session (%s): %v", session, err)
		}
		return session, nil
	}
	return session, nil
}

func (s *storedSessionLoader) retryLoadingValidSession(req *http.Request, attempts int) (*sessionsapi.SessionState, error) {
	for i := 0; i < attempts; i++ {
		session, err := s.store.Load(req)
		if err != nil {
			return nil, err
		}
		if session == nil {
			// No session was found in the storage, nothing more to do
			return nil, nil
		}

		if !s.isRefreshPeriodOver(session) {
			// Refresh is disabled or the session is not old enough, do nothing
			return session, nil
		}

		if !s.isRefreshSessionNeededWithProvider(session) {
			return session, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, nil
}

func (s *storedSessionLoader) isRefreshPeriodOver(session *sessionsapi.SessionState) bool {
	return s.refreshPeriod > time.Duration(0) && session.Age() >= s.refreshPeriod
}

// refreshSession attemptValidSession to refresh the session with the provider
// and will save the session if it was updated.
func (s *storedSessionLoader) refreshSession(rw http.ResponseWriter, req *http.Request, session *sessionsapi.SessionState) error {
	err := s.refreshSessionWithProvider(req.Context(), session)
	if err != nil {
		return fmt.Errorf("error refreshing access token: %v", err)
	}

	if session == nil {
		return nil
	}

	// Because the session was refreshed, make sure to save it
	err = s.store.Save(rw, req, session)
	if err != nil {
		logger.PrintAuthf(session.Email, req, logger.AuthError, "error saving session: %v", err)
		return fmt.Errorf("error saving session: %v", err)
	}
	return nil
}

// validateSession checks whether the session has expired and performs
// provider validation on the session.
// An error implies the session is not longer valid.
func (s *storedSessionLoader) validateSession(ctx context.Context, session *sessionsapi.SessionState) error {
	if session.IsExpired() {
		return errors.New("session is expired")
	}

	if !s.validateSessionState(ctx, session) {
		return errors.New("session is invalid")
	}

	return nil
}
