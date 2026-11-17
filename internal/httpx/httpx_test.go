package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// respond serves every request with the given status, content type and body, recording the
// last request it saw.
func respond(t *testing.T, status int, contentType, body string) (url string, last func() *http.Request) {
	t.Helper()
	var mu sync.Mutex // the handler runs on the server's goroutine
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r
		mu.Unlock()
		w.Header().Set("Link", `<next>; rel="next"`)
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/thing", func() *http.Request {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

type thing struct {
	Name string `json:"name"`
}

func TestGetJSONDecodes(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/vnd.github+json"} {
		url, _ := respond(t, http.StatusOK, ct, `{"name":"yoink"}`)
		var out thing
		h, err := (&Client{}).GetJSON(context.Background(), url, &out)
		if err != nil {
			t.Errorf("%s: %v", ct, err)
			continue
		}
		if out.Name != "yoink" {
			t.Errorf("%s: decoded %+v", ct, out)
		}
		if h.Get("Link") != `<next>; rel="next"` {
			t.Errorf("%s: headers not returned: %v", ct, h)
		}
	}
}

func TestGetJSONSendsHeaders(t *testing.T) {
	url, last := respond(t, http.StatusOK, "application/json", `{}`)
	c := &Client{Header: http.Header{"Authorization": {"Bearer x"}, "Accept": {"application/vnd.github+json"}}}
	if _, err := c.GetJSON(context.Background(), url, &thing{}); err != nil {
		t.Fatal(err)
	}
	r := last()
	for k, want := range map[string]string{"Authorization": "Bearer x", "Accept": "application/vnd.github+json", "User-Agent": "yoink"} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if r.Method != http.MethodGet {
		t.Errorf("method = %s", r.Method)
	}

	// Without an Accept header of its own, the client asks for JSON.
	if _, err := (&Client{}).GetJSON(context.Background(), url, &thing{}); err != nil {
		t.Fatal(err)
	}
	if got := last().Header.Get("Accept"); got != "application/json" {
		t.Errorf("default Accept = %q", got)
	}
	// The client's header map is never modified by a request.
	if len(c.Header) != 2 {
		t.Errorf("client header changed: %v", c.Header)
	}
}

func TestGetJSONErrors(t *testing.T) {
	for _, c := range []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{"API message", http.StatusNotFound, "application/json", `{"message":"Not Found"}`, "HTTP 404: Not Found"},
		{"API message on 403", http.StatusForbidden, "application/vnd.github+json", `{"message":"API rate limit exceeded"}`, "HTTP 403: API rate limit exceeded"},
		{"JSON without a message", http.StatusInternalServerError, "application/json", `{"error":"x"}`, "HTTP 500: Internal Server Error"},
		{"HTML error page", http.StatusBadGateway, "text/html", "<html>bad gateway</html>", "HTTP 502: Bad Gateway"},
		{"sign-in page", http.StatusNonAuthoritativeInfo, "text/html; charset=utf-8", "<html>sign in</html>", "HTTP 203: expected JSON, got text/html (not signed in?)"},
	} {
		url, _ := respond(t, c.status, c.contentType, c.body)
		h, err := (&Client{}).GetJSON(context.Background(), url, &thing{})
		var he *Error
		if !errors.As(err, &he) {
			t.Errorf("%s: err = %v (%T), want *Error", c.name, err, err)
			continue
		}
		if he.StatusCode != c.status || err.Error() != c.want {
			t.Errorf("%s: err = %q (status %d), want %q", c.name, err, he.StatusCode, c.want)
		}
		if h.Get("Link") == "" {
			t.Errorf("%s: headers not returned with the error", c.name)
		}
	}
}

func TestGetJSONMalformedBody(t *testing.T) {
	url, _ := respond(t, http.StatusOK, "application/json", `{"name":`)
	_, err := (&Client{}).GetJSON(context.Background(), url, &thing{})
	if err == nil || !strings.HasPrefix(err.Error(), "decoding "+url+": ") {
		t.Fatalf("err = %v", err)
	}
	var he *Error
	if errors.As(err, &he) {
		t.Error("a decoding failure is not an HTTP error")
	}
}

func TestGetJSONTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := (&Client{}).GetJSON(context.Background(), url, &thing{}); err == nil {
		t.Error("want an error from a closed server")
	}

	live, _ := respond(t, http.StatusOK, "application/json", `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Client{}).GetJSON(ctx, live, &thing{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v, want context.Canceled", err)
	}

	if _, err := (&Client{}).GetJSON(context.Background(), "http://bad host/", &thing{}); err == nil {
		t.Error("want an error for an invalid URL")
	}
}

func TestGetJSONUsesTheGivenClient(t *testing.T) {
	url, _ := respond(t, http.StatusOK, "application/json", `{"name":"x"}`)
	used := false
	hc := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := (&Client{HTTP: hc}).GetJSON(context.Background(), url, &thing{}); err != nil || !used {
		t.Errorf("err = %v, custom client used = %v", err, used)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestErrorString(t *testing.T) {
	for _, c := range []struct {
		err  Error
		want string
	}{
		{Error{StatusCode: 404}, "HTTP 404: Not Found"},
		{Error{StatusCode: 404, Message: "no such org"}, "HTTP 404: no such org"},
		{Error{StatusCode: 599}, "HTTP 599"}, // no standard name to show
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.err, got, c.want)
		}
	}
}
