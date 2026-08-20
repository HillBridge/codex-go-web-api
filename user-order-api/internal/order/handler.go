package order

import (
	"context"
	"net/http"
	"strings"
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
		request, err := httpx.ParsePageRequest(r)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		orders, err := h.service.List(ctx, request)
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

		_, input.IdempotencyKeyProvided = r.Header["Idempotency-Key"]
		input.IdempotencyKey = r.Header.Get("Idempotency-Key")
		order, replayed, err := h.service.Create(ctx, input)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		httpx.WriteJSON(w, status, order)
	default:
		httpx.WriteMethodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) orderByID(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()

	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/orders/"), "/")
	parts := strings.Split(path, "/")
	id, err := httpx.PathID("/orders/"+parts[0], "/orders/")
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httpx.WriteMethodNotAllowed(w, "GET")
			return
		}
		order, err := h.service.FindByID(ctx, id)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, order)
		return
	}
	if len(parts) != 2 {
		httpx.WriteError(w, httpx.NotFoundCode("ROUTE_NOT_FOUND", "route not found"))
		return
	}
	if r.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(w, "POST")
		return
	}

	var order Order
	switch parts[1] {
	case "pay":
		order, err = h.service.Pay(ctx, id)
	case "cancel":
		order, err = h.service.Cancel(ctx, id)
	default:
		httpx.WriteError(w, httpx.NotFoundCode("ROUTE_NOT_FOUND", "route not found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, order)
}

func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Second)
}
