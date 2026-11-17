package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A response cut off mid-body is an error, not a silently short decode.
func TestGetJSONTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"name":`)) // the server closes the connection 92 bytes short
	}))
	t.Cleanup(srv.Close)

	var out thing
	h, err := (&Client{}).GetJSON(context.Background(), srv.URL, &out)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want an unexpected EOF", err)
	}
	if h == nil || h.Get("Content-Length") != "100" {
		t.Errorf("headers = %v, want the response's headers", h)
	}
}
