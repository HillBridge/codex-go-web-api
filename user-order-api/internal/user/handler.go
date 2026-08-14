package user

import (
	"context"
	"net/http"
	"time"

	"bridge-go/user-order-api/internal/platform/httpx"
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

	switch r.Method {
	case http.MethodGet:
		users, err := h.service.List(ctx)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, users)
	case http.MethodPost:
		var input CreateUserRequest
		if err := httpx.DecodeJSON(r, &input); err != nil {
			httpx.WriteError(w, httpx.BadRequest("invalid JSON body"))
			return
		}

		user, err := h.service.Create(ctx, input)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, user)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) userByID(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	id, err := httpx.PathID(r.URL.Path, "/users/")
	if err != nil {
		httpx.WriteError(w, err)
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
