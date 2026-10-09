package apiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tight-Line/skrat/core"
)

func TestDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Authorization") != "Bearer t" || r.URL.Query().Get("a") != "1" || r.URL.Query().Get("b") != "2" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"name":"x"}`))
		case "/limited":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("no such thing"))
		}
	}))
	defer srv.Close()
	c := &Client{Name: "test", BaseURL: srv.URL, Auth: func(r *http.Request) { r.Header.Set("Authorization", "Bearer t") }}
	ctx := context.Background()

	var out struct{ Name string }
	if err := c.Do(ctx, "GET", "/ok?a=1", url.Values{"b": {"2"}}, nil, &out); err != nil || out.Name != "x" {
		t.Fatalf("ok: %v %+v", err, out)
	}

	var retry *core.RetryableError
	if err := c.Do(ctx, "GET", "/limited", nil, nil, nil); !errors.As(err, &retry) {
		t.Errorf("429 must be retryable: %v", err)
	}

	err := c.Do(ctx, "GET", "/missing", nil, nil, nil)
	if !IsStatus(err, http.StatusNotFound) || errors.As(err, &retry) {
		t.Errorf("404 must be a plain StatusError: %v", err)
	}
	if err.Error() != "test: GET /missing: 404 no such thing" {
		t.Errorf("message: %q", err)
	}
}

func TestMissingCAHint(t *testing.T) {
	// httptest's certificate is signed by a CA no system trusts, which is
	// exactly what every server looks like on a host without CA certificates.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	err := (&Client{Name: "test", BaseURL: srv.URL}).Do(context.Background(), "GET", "/", nil, nil, nil)
	var retry *core.RetryableError
	if err == nil || !strings.Contains(err.Error(), "ca-certificates") || errors.As(err, &retry) {
		t.Fatalf("want a non-retryable error with a hint: %v", err)
	}
}
