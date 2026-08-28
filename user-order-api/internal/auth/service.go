package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/outbox"
)

type RegisterRequest struct {
	Name     string
	Email    string
	Password string
}

type LoginRequest struct {
	Email    string
	Password string
}

type Result struct {
	Identity     Identity
	AccessToken  string
	RefreshToken string
}

type Service struct {
	identities IdentityRepository
	sessions   Repository
	tokens     *TokenManager
	refreshTTL time.Duration
	now        func() time.Time
	audit      audit.Logger
}

func NewService(identities IdentityRepository, sessions Repository, tokens *TokenManager, refreshTTL time.Duration, now func() time.Time) *Service {
	return &Service{identities: identities, sessions: sessions, tokens: tokens, refreshTTL: refreshTTL, now: now}
}

func (s *Service) RequireBearer(next http.Handler) http.Handler {
	return requireBearer(s.tokens, s.sessions, s.now, next)
}

func (s *Service) SetAuditLogger(logger audit.Logger) { s.audit = logger }

func (s *Service) Register(ctx context.Context, input RegisterRequest) (Result, error) {
	if strings.TrimSpace(input.Name) == "" || !strings.Contains(input.Email, "@") {
		return Result{}, httpx.BadRequest("name and valid email are required")
	}
	if length := len([]byte(input.Password)); length < 12 || length > 72 {
		return Result{}, httpx.BadRequest("password must be between 12 and 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return Result{}, httpx.Internal("failed to hash password", err)
	}
	identityInput := NewIdentity{Name: input.Name, Email: input.Email, PasswordHash: string(hash), Role: RoleUser}
	refresh, err := newRefreshToken()
	if err != nil {
		return Result{}, httpx.Internal("failed to create refresh token", err)
	}
	newSession := NewSession{ID: newSessionID(), TokenHash: tokenHash(refresh), ExpiresAt: s.now().Add(s.refreshTTL)}
	if repo, ok := s.identities.(*MySQLRepository); ok && repo.events != nil {
		identity, session, eventPersisted, err := repo.RegisterWithEvent(ctx, identityInput, newSession, func(identity Identity, session Session) (outbox.Event, error) {
			return outbox.NewEvent("auth.registered", "user", identity.ID, map[string]any{"userID": identity.ID, "sessionID": session.ID}, s.now())
		})
		if err != nil {
			if errors.Is(err, ErrEmailTaken) {
				return Result{}, httpx.ConflictCode("EMAIL_ALREADY_EXISTS", "email already exists")
			}
			return Result{}, httpx.Internal("failed to create user", fmt.Errorf("create identity: %w", err))
		}
		result, err := s.resultFromSession(identity, session, refresh)
		if err == nil && !eventPersisted && s.audit != nil {
			s.audit.Record(ctx, "auth.registered", map[string]any{"userID": identity.ID})
		}
		return result, err
	}
	identity, err := s.identities.CreateIdentity(ctx, identityInput)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return Result{}, httpx.ConflictCode("EMAIL_ALREADY_EXISTS", "email already exists")
		}
		return Result{}, httpx.Internal("failed to create user", fmt.Errorf("create identity: %w", err))
	}
	result, err := s.newSessionResult(ctx, identity)
	if err == nil && s.audit != nil {
		s.audit.Record(ctx, "auth.registered", map[string]any{"userID": identity.ID})
	}
	return result, err
}

func (s *Service) Login(ctx context.Context, input LoginRequest) (Result, error) {
	identity, err := s.identities.FindIdentityByEmail(ctx, input.Email)
	if err != nil || identity.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(input.Password))
		return Result{}, invalidCredentials()
	}
	if bcrypt.CompareHashAndPassword([]byte(identity.PasswordHash), []byte(input.Password)) != nil {
		return Result{}, invalidCredentials()
	}
	refresh, err := newRefreshToken()
	if err != nil {
		return Result{}, httpx.Internal("failed to create refresh token", err)
	}
	newSession := NewSession{ID: newSessionID(), UserID: identity.ID, TokenHash: tokenHash(refresh), ExpiresAt: s.now().Add(s.refreshTTL)}
	if repo, ok := s.sessions.(*MySQLRepository); ok && repo.events != nil {
		session, eventPersisted, err := repo.CreateSessionWithEvent(ctx, newSession, func(session Session) (outbox.Event, error) {
			return outbox.NewEvent("auth.logged_in", "user", identity.ID, map[string]any{"userID": identity.ID, "sessionID": session.ID}, s.now())
		})
		if err != nil {
			return Result{}, httpx.Internal("failed to create session", err)
		}
		result, err := s.resultFromSession(identity, session, refresh)
		if err == nil && !eventPersisted && s.audit != nil {
			s.audit.Record(ctx, "auth.logged_in", map[string]any{"userID": identity.ID, "sessionID": session.ID})
		}
		return result, err
	}
	result, err := s.newSessionResult(ctx, identity)
	if err == nil && s.audit != nil {
		s.audit.Record(ctx, "auth.logged_in", map[string]any{"userID": identity.ID})
	}
	return result, err
}

func (s *Service) Refresh(ctx context.Context, rawToken string) (Result, error) {
	session, err := s.sessions.FindActiveByTokenHash(ctx, tokenHash(rawToken), s.now())
	if err != nil {
		return Result{}, unauthenticated()
	}
	identity, err := s.identities.FindIdentityByID(ctx, session.UserID)
	if err != nil {
		return Result{}, unauthenticated()
	}
	refresh, err := newRefreshToken()
	if err != nil {
		return Result{}, httpx.Internal("failed to create refresh token", err)
	}
	replacement := NewSession{ID: newSessionID(), UserID: identity.ID, TokenHash: tokenHash(refresh), ExpiresAt: s.now().Add(s.refreshTTL)}
	var newSession Session
	eventPersisted := false
	if repo, ok := s.sessions.(*MySQLRepository); ok && repo.events != nil {
		newSession, eventPersisted, err = repo.RotateWithEvent(ctx, session.ID, replacement, func(session Session) (outbox.Event, error) {
			return outbox.NewEvent("auth.refreshed", "user", identity.ID, map[string]any{"userID": identity.ID, "sessionID": session.ID}, s.now())
		})
	} else {
		newSession, err = s.sessions.Rotate(ctx, session.ID, replacement)
	}
	if err != nil {
		return Result{}, unauthenticated()
	}
	access, err := s.tokens.Issue(Principal{UserID: identity.ID, Role: string(identity.Role), SessionID: newSession.ID, AuthVersion: identity.AuthVersion})
	if err != nil {
		return Result{}, httpx.Internal("failed to issue access token", err)
	}
	result := Result{Identity: identity, AccessToken: access, RefreshToken: refresh}
	if !eventPersisted && s.audit != nil {
		s.audit.Record(ctx, "auth.refreshed", map[string]any{"userID": identity.ID, "sessionID": newSession.ID})
	}
	return result, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	session, err := s.sessions.FindActiveByTokenHash(ctx, tokenHash(rawToken), s.now())
	if err != nil {
		return unauthenticated()
	}
	if repo, ok := s.sessions.(*MySQLRepository); ok && repo.events != nil {
		if _, err := repo.RevokeWithEvent(ctx, session.ID, s.now(), func(session Session) (outbox.Event, error) {
			return outbox.NewEvent("auth.logged_out", "user", session.UserID, map[string]any{"userID": session.UserID, "sessionID": session.ID}, s.now())
		}); err != nil {
			return unauthenticated()
		}
	} else if err := s.sessions.Revoke(ctx, session.ID, s.now()); err != nil {
		return unauthenticated()
	} else if s.audit != nil {
		s.audit.Record(ctx, "auth.logged_out", map[string]any{"userID": session.UserID, "sessionID": session.ID})
	}
	return nil
}

func (s *Service) Me(ctx context.Context, userID int64) (Identity, error) {
	identity, err := s.identities.FindIdentityByID(ctx, userID)
	if err != nil {
		return Identity{}, unauthenticated()
	}
	return identity, nil
}

func (s *Service) BootstrapAdmin(ctx context.Context, email, password string) (bool, error) {
	if !strings.Contains(email, "@") || len([]byte(password)) < 12 || len([]byte(password)) > 72 {
		return false, httpx.BadRequest("bootstrap admin email and password are invalid")
	}
	_, err := s.identities.FindIdentityByEmail(ctx, email)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		return false, httpx.Internal("failed to find bootstrap admin", fmt.Errorf("find bootstrap admin: %w", err))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, httpx.Internal("failed to hash bootstrap password", err)
	}
	_, err = s.identities.CreateIdentity(ctx, NewIdentity{Name: "Bootstrap Admin", Email: email, PasswordHash: string(hash), Role: RoleAdmin})
	if errors.Is(err, ErrEmailTaken) {
		return false, nil
	}
	if err != nil {
		return false, httpx.Internal("failed to create bootstrap admin", fmt.Errorf("create bootstrap admin: %w", err))
	}
	return true, nil
}

func (s *Service) newSessionResult(ctx context.Context, identity Identity) (Result, error) {
	refresh, err := newRefreshToken()
	if err != nil {
		return Result{}, httpx.Internal("failed to create refresh token", err)
	}
	session, err := s.sessions.Create(ctx, NewSession{ID: newSessionID(), UserID: identity.ID, TokenHash: tokenHash(refresh), ExpiresAt: s.now().Add(s.refreshTTL)})
	if err != nil {
		return Result{}, httpx.Internal("failed to create session", err)
	}
	access, err := s.tokens.Issue(Principal{UserID: identity.ID, Role: string(identity.Role), SessionID: session.ID, AuthVersion: identity.AuthVersion})
	if err != nil {
		return Result{}, httpx.Internal("failed to issue access token", err)
	}
	return Result{Identity: identity, AccessToken: access, RefreshToken: refresh}, nil
}

func (s *Service) resultFromSession(identity Identity, session Session, refresh string) (Result, error) {
	access, err := s.tokens.Issue(Principal{UserID: identity.ID, Role: string(identity.Role), SessionID: session.ID, AuthVersion: identity.AuthVersion})
	if err != nil {
		return Result{}, httpx.Internal("failed to issue access token", err)
	}
	return Result{Identity: identity, AccessToken: access, RefreshToken: refresh}, nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func newRefreshToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func newSessionID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(value[:4]), hex.EncodeToString(value[4:6]), hex.EncodeToString(value[6:8]), hex.EncodeToString(value[8:10]), hex.EncodeToString(value[10:]))
}
func invalidCredentials() error {
	return httpx.UnauthorizedCode("INVALID_CREDENTIALS", "invalid credentials")
}
func unauthenticated() error { return httpx.UnauthorizedCode("UNAUTHENTICATED", "unauthenticated") }
