package auth

import (
	"context"
	"net/http"
	"strings"

	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/principal"
)

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	return principal.FromContext(ctx)
}

func RequireBearer(tokens *TokenManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteError(w, unauthenticated())
			return
		}
		currentPrincipal, err := tokens.Parse(parts[1])
		if err != nil {
			httpx.WriteError(w, unauthenticated())
			return
		}
		next.ServeHTTP(w, r.WithContext(principal.WithContext(r.Context(), currentPrincipal)))
	})
}
