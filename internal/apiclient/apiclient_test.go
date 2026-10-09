package apiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Tight-Line/heinzel/core"
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
