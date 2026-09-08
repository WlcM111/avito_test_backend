package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/avito-kitchen/platform/pkg/logx"
)

const contentTypeJSON = "application/json; charset=utf-8"

type errorBody struct {
	Code      string           `json:"code"`
	Message   string           `json:"message"`
	Details   []FieldViolation `json:"details,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// WriteJSON serialises v with the given status code.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logx.From(r.Context(), slog.Default()).Error("write response body", slog.Any("error", err))
	}
}

// WriteNoContent finishes the response with 204.
func WriteNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// WriteError converts err into the shared error envelope. Unknown errors are
// reported as 500 without leaking the internal cause; the cause is logged.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		apiErr = Internal(err)
	}
	ctx := r.Context()
	if apiErr.Status >= http.StatusInternalServerError {
		logx.From(ctx, logger).Error("request failed",
			slog.String("code", apiErr.Code), slog.Any("error", err))
	} else {
		logx.From(ctx, logger).Warn("request rejected",
			slog.String("code", apiErr.Code), slog.String("message", apiErr.Message))
	}
	WriteJSON(w, r, apiErr.Status, errorEnvelope{Error: errorBody{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Details:   apiErr.Details,
		RequestID: logx.RequestID(ctx),
	}})
}

// DecodeJSON reads a JSON request body into dst. It rejects a wrong content
// type, malformed JSON, unknown fields and trailing content.
func DecodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if base := strings.TrimSpace(strings.Split(ct, ";")[0]); base != "application/json" {
			return NewError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia,
				"content type must be application/json")
		}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				"request body is too large")
		}
		return BadRequest("request body is not valid JSON: " + err.Error())
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BadRequest("request body must contain a single JSON object")
	}
	return nil
}
