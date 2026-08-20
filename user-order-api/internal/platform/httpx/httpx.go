package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"bridge-go/user-order-api/internal/platform/page"
)

const MaxJSONBodyBytes int64 = 1 << 20

type AppError struct {
	Status  int
	Code    string
	Message string
	Err     error
}

const (
	CodeInvalidRequest        = "INVALID_REQUEST"
	CodeNotFound              = "NOT_FOUND"
	CodeInternalError         = "INTERNAL_ERROR"
	CodeUnsupportedMediaType  = "UNSUPPORTED_MEDIA_TYPE"
	CodeRequestEntityTooLarge = "REQUEST_ENTITY_TOO_LARGE"
	CodeMethodNotAllowed      = "METHOD_NOT_ALLOWED"
)

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
	return BadRequestCode(CodeInvalidRequest, message)
}

func BadRequestCode(code, message string) *AppError {
	return &AppError{Status: http.StatusBadRequest, Code: code, Message: message}
}

func NotFound(message string) *AppError {
	return NotFoundCode(CodeNotFound, message)
}

func NotFoundCode(code, message string) *AppError {
	return &AppError{Status: http.StatusNotFound, Code: code, Message: message}
}

func Internal(message string, err error) *AppError {
	return &AppError{Status: http.StatusInternalServerError, Code: CodeInternalError, Message: message, Err: err}
}

func UnsupportedMediaType(message string) *AppError {
	return &AppError{Status: http.StatusUnsupportedMediaType, Code: CodeUnsupportedMediaType, Message: message}
}

func RequestEntityTooLarge(message string) *AppError {
	return &AppError{Status: http.StatusRequestEntityTooLarge, Code: CodeRequestEntityTooLarge, Message: message}
}

func MethodNotAllowed() *AppError {
	return &AppError{Status: http.StatusMethodNotAllowed, Code: CodeMethodNotAllowed, Message: "method not allowed"}
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, err error) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		WriteJSON(w, appErr.Status, errorResponse{Code: appErr.Code, Error: appErr.Message})
		return
	}

	WriteJSON(w, http.StatusInternalServerError, errorResponse{Code: CodeInternalError, Error: "internal server error"})
}

type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
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

func ParsePageRequest(r *http.Request) (page.Request, error) {
	request := page.Request{Limit: 20}
	query := r.URL.Query()

	if rawLimit := query.Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 100 {
			return page.Request{}, BadRequest("limit must be between 1 and 100")
		}
		request.Limit = limit
	}

	if rawAfterID := query.Get("afterId"); rawAfterID != "" {
		afterID, err := strconv.ParseInt(rawAfterID, 10, 64)
		if err != nil || afterID <= 0 {
			return page.Request{}, BadRequest("afterId must be a positive integer")
		}
		request.AfterID = afterID
	}

	return request, nil
}
