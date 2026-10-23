package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
)

// Request body problems, as client-safe messages.
const (
	msgUnsupportedMediaType = "content type must be application/json"
	msgBodyTooLarge         = "request body too large"
	msgBodyUnreadable       = "could not read request body"
)

// readJSONBody reads a JSON request body of at most limit bytes. On
// failure, it returns the status and a client-safe message.
func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, int, string) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return nil, http.StatusUnsupportedMediaType, msgUnsupportedMediaType
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			return nil, http.StatusBadRequest, msgBodyTooLarge
		}
		return nil, http.StatusBadRequest, msgBodyUnreadable
	}
	return body, 0, ""
}
