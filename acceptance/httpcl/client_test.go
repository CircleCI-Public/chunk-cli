package httpcl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/acceptance/httpcl"
)

func TestCallJSONRoundTrip(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	type respBody struct {
		Greeting string `json:"greeting"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("expected JSON content-type, got %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected bearer auth, got %q", r.Header.Get("Authorization"))
		}

		var body reqBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(respBody{Greeting: "hello " + body.Name})
	}))
	defer srv.Close()

	c := httpcl.New(httpcl.Config{
		BaseURL:   srv.URL,
		AuthToken: "test-token",
	})

	var resp respBody
	status, err := c.Call(context.Background(), httpcl.NewRequest("POST", "/test",
		httpcl.Body(reqBody{Name: "world"}),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Greeting != "hello world" {
		t.Fatalf("expected 'hello world', got %q", resp.Greeting)
	}
}

func TestCallHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := httpcl.New(httpcl.Config{BaseURL: srv.URL})

	status, err := c.Call(context.Background(), httpcl.NewRequest("GET", "/missing"))
	if status != 404 {
		t.Fatalf("expected 404, got %d", status)
	}
	if !httpcl.HasStatusCode(err, http.StatusNotFound) {
		t.Fatalf("expected HTTPError with 404, got %v", err)
	}
}

func TestCallCustomAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "my-key" {
			t.Errorf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := httpcl.New(httpcl.Config{
		BaseURL:    srv.URL,
		AuthToken:  "my-key",
		AuthHeader: "x-api-key",
	})

	status, err := c.Call(context.Background(), httpcl.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}
