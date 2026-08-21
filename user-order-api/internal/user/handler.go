package user

import (
	"context"
	"net/http"
	"time"

	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/principal"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/users", h.users)
	mux.HandleFunc("/users/", h.userByID)
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	currentPrincipal, ok := principal.FromContext(ctx)
	if !ok {
		httpx.WriteError(w, httpx.UnauthorizedCode("UNAUTHENTICATED", "unauthenticated"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !principal.IsAdmin(currentPrincipal) {
			httpx.WriteError(w, httpx.ForbiddenCode("FORBIDDEN", "insufficient permissions"))
			return
		}
		request, err := httpx.ParsePageRequest(r)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		users, err := h.service.List(ctx, request)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, users)
	case http.MethodPost:
		if !principal.IsAdmin(currentPrincipal) {
			httpx.WriteError(w, httpx.ForbiddenCode("FORBIDDEN", "insufficient permissions"))
			return
		}
		var input CreateUserRequest
		if err := httpx.DecodeJSON(w, r, &input); err != nil {
			httpx.WriteError(w, err)
			return
		}

		user, err := h.service.Create(ctx, input)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, user)
	default:
		httpx.WriteMethodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) userByID(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	currentPrincipal, ok := principal.FromContext(ctx)
	if !ok {
		httpx.WriteError(w, httpx.UnauthorizedCode("UNAUTHENTICATED", "unauthenticated"))
		return
	}

	if r.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(w, "GET")
		return
	}

	id, err := httpx.PathID(r.URL.Path, "/users/")
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if !principal.IsAdmin(currentPrincipal) && currentPrincipal.UserID != id {
		httpx.WriteError(w, httpx.ForbiddenCode("FORBIDDEN", "insufficient permissions"))
		return
	}
	user, err := h.service.FindByID(ctx, id)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, user)
}

func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Second)
}
