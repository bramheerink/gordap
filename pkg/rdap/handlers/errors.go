package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

const contentType = "application/rdap+json"

// writeError emits an RFC 9083 §6 error response with the matching HTTP
// code. Top-level notices (e.g. ICANN ToS / status codes / WICF) are
// carried on error responses too because clients rely on the notices
// array to point users at support and ToS.
func writeError(w http.ResponseWriter, status int, title string, notices []types.Notice, desc ...string) {
	resp := types.Error{
		Common: types.Common{
			RDAPConformance: types.DefaultConformance,
			ObjectClassName: "error",
			Notices:         notices,
		},
		ErrorCode:   status,
		Title:       title,
		Description: desc,
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// statusFor translates storage errors into HTTP status codes. Unknown
// errors become 500 so genuine bugs are not masked.
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, datasource.ErrNotFound):
		return http.StatusNotFound, "Object not found"
	case errors.Is(err, datasource.ErrUnauthorized):
		return http.StatusForbidden, "Access denied"
	default:
		return http.StatusInternalServerError, "Internal server error"
	}
}
