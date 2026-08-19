package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const MaxJSONBodyBytes int64 = 1 << 20

type AppError struct {
	Status  int
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func BadRequest(message string) *AppError {
	return &AppError{Status: http.StatusBadRequest, Message: message}
}

func NotFound(message string) *AppError {
	return &AppError{Status: http.StatusNotFound, Message: message}
}

func Internal(message string, err error) *AppError {
	return &AppError{Status: http.StatusInternalServerError, Message: message, Err: err}
}

func UnsupportedMediaType(message string) *AppError {
	return &AppError{Status: http.StatusUnsupportedMediaType, Message: message}
}

func RequestEntityTooLarge(message string) *AppError {
	return &AppError{Status: http.StatusRequestEntityTooLarge, Message: message}
}

func MethodNotAllowed() *AppError {
	return &AppError{Status: http.StatusMethodNotAllowed, Message: "method not allowed"}
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, err error) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		WriteJSON(w, appErr.Status, map[string]string{"error": appErr.Message})
		return
	}

	WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func WriteMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	WriteError(w, MethodNotAllowed())
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return UnsupportedMediaType("Content-Type must be application/json")
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return decodeError(err)
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return BadRequest("request body must contain a single JSON object")
		}
		return decodeError(err)
	}

	return nil
}

func decodeError(err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return RequestEntityTooLarge("request body too large")
	}
	return BadRequest("invalid JSON body")
}

func PathID(path, prefix string) (int64, error) {
	raw := strings.TrimPrefix(path, prefix)
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return 0, BadRequest("missing id")
	}

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, BadRequest("invalid id")
	}

	return id, nil
}
