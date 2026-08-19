package order

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
	mux.HandleFunc("/orders", h.orders)
	mux.HandleFunc("/orders/", h.orderByID)
}

func (h *Handler) orders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()

	switch r.Method {
	case http.MethodGet:
		orders, err := h.service.List(ctx)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, orders)
	case http.MethodPost:
		var input CreateOrderRequest
		if err := httpx.DecodeJSON(w, r, &input); err != nil {
			httpx.WriteError(w, err)
			return
		}

		order, err := h.service.Create(ctx, input)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, order)
	default:
		httpx.WriteMethodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) orderByID(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()

	if r.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(w, "GET")
		return
	}

	id, err := httpx.PathID(r.URL.Path, "/orders/")
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	order, err := h.service.FindByID(ctx, id)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, order)
}

func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Second)
}
