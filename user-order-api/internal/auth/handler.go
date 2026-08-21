package auth

import (
	"net/http"
	"time"

	"bridge-go/user-order-api/internal/platform/httpx"
)

const refreshCookieName = "refresh_token"

type Handler struct {
	service      *Service
	cookieSecure bool
}

func NewHandler(service *Service, cookieSecure bool) *Handler {
	return &Handler{service: service, cookieSecure: cookieSecure}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/auth/register", h.register)
	mux.HandleFunc("/auth/login", h.login)
	mux.HandleFunc("/auth/refresh", h.refresh)
	mux.HandleFunc("/auth/logout", h.logout)
	mux.Handle("/auth/me", h.service.RequireBearer(http.HandlerFunc(h.me)))
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(w, "POST")
		return
	}
	var input RegisterRequest
	if err := httpx.DecodeJSON(w, r, &input); err != nil {
		httpx.WriteError(w, err)
		return
	}
	result, err := h.service.Register(r.Context(), input)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	h.writeResult(w, http.StatusCreated, result)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(w, "POST")
		return
	}
	var input LoginRequest
	if err := httpx.DecodeJSON(w, r, &input); err != nil {
		httpx.WriteError(w, err)
		return
	}
	result, err := h.service.Login(r.Context(), input)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	h.writeResult(w, http.StatusOK, result)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(w, "POST")
		return
	}
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		httpx.WriteError(w, unauthenticated())
		return
	}
	result, err := h.service.Refresh(r.Context(), cookie.Value)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	h.writeResult(w, http.StatusOK, result)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(w, "POST")
		return
	}
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		httpx.WriteError(w, unauthenticated())
		return
	}
	if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
		httpx.WriteError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: "", Path: "/api/v1/auth", HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(w, "GET")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthenticated())
		return
	}
	identity, err := h.service.Me(r.Context(), principal.UserID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  Role   `json:"role"`
	}{ID: identity.ID, Name: identity.Name, Email: identity.Email, Role: identity.Role})
}

func (h *Handler) writeResult(w http.ResponseWriter, status int, result Result) {
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: result.RefreshToken, Path: "/api/v1/auth", HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: int((7 * 24 * time.Hour).Seconds())})
	httpx.WriteJSON(w, status, struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
			Role  Role   `json:"role"`
		} `json:"user"`
	}{AccessToken: result.AccessToken, User: struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  Role   `json:"role"`
	}{ID: result.Identity.ID, Name: result.Identity.Name, Email: result.Identity.Email, Role: result.Identity.Role}})
}
