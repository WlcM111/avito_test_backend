package httpx

import (
	"net/http"
	"strconv"
	"strings"
)

// Page holds validated pagination parameters of a list endpoint.
type Page struct {
	Limit  int
	Offset int
}

// ParsePage reads limit and offset from the query string and validates them
// against the shared contract limits.
func ParsePage(r *http.Request, defLimit, maxLimit int) (Page, error) {
	p := Page{Limit: defLimit}
	q := r.URL.Query()

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return p, Unprocessable("limit must be an integer").
				WithDetails(FieldViolation{Field: "limit", Reason: "not an integer"})
		}
		if v < 1 || v > maxLimit {
			return p, Unprocessable("limit is out of range").
				WithDetails(FieldViolation{
					Field:  "limit",
					Reason: "must be between 1 and " + strconv.Itoa(maxLimit),
				})
		}
		p.Limit = v
	}

	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return p, Unprocessable("offset must be an integer").
				WithDetails(FieldViolation{Field: "offset", Reason: "not an integer"})
		}
		if v < 0 {
			return p, Unprocessable("offset must not be negative").
				WithDetails(FieldViolation{Field: "offset", Reason: "must be >= 0"})
		}
		p.Offset = v
	}
	return p, nil
}
