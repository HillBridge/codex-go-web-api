package auth

import (
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"bridge-go/user-order-api/internal/platform/principal"
)

type Principal = principal.Principal

type TokenManager struct {
	signingKey []byte
	issuer     string
	ttl        time.Duration
	now        func() time.Time
}

type accessClaims struct {
	Role        string `json:"role"`
	SessionID   string `json:"sid"`
	AuthVersion int64  `json:"ver"`
	jwt.RegisteredClaims
}

func NewTokenManager(signingKey []byte, issuer string, ttl time.Duration, now func() time.Time) *TokenManager {
	return &TokenManager{signingKey: signingKey, issuer: issuer, ttl: ttl, now: now}
}

func (m *TokenManager) Issue(principal Principal) (string, error) {
	now := m.now().UTC()
	claims := accessClaims{
		Role:        principal.Role,
		SessionID:   principal.SessionID,
		AuthVersion: principal.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   strconv.FormatInt(principal.UserID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.signingKey)
}

func (m *TokenManager) Parse(raw string) (Principal, error) {
	claims := accessClaims{}
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %q", token.Method.Alg())
		}
		return m.signingKey, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithTimeFunc(m.now))
	if err != nil || !token.Valid {
		if err == nil {
			err = fmt.Errorf("invalid access token")
		}
		return Principal{}, err
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || userID <= 0 || claims.Role == "" || claims.SessionID == "" {
		return Principal{}, fmt.Errorf("invalid access token claims")
	}
	return Principal{UserID: userID, Role: claims.Role, SessionID: claims.SessionID, AuthVersion: claims.AuthVersion}, nil
}
