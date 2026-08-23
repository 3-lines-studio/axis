package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetadataRoundTrip(t *testing.T) {
	a := app{root: t.TempDir()}
	id := strings.Repeat("a", 24)
	want := metadata{ID: id, Title: "Test", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := a.writeMetadata(want); err != nil {
		t.Fatal(err)
	}
	got, err := a.readMetadata(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != want.Title {
		t.Fatalf("title = %q", got.Title)
	}
}

func TestSessionPathRejectsTraversal(t *testing.T) {
	a := app{root: t.TempDir()}
	if _, err := a.sessionPath("../session"); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestSecureRequiresCredentials(t *testing.T) {
	a := app{username: "user", password: "secret"}
	handler := a.secure(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("user", "secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRunReplaysEvents(t *testing.T) {
	run := &run{watchers: make(map[chan string]struct{})}
	run.publish("delta", "hello")
	run.finish()
	if len(run.events) != 2 {
		t.Fatalf("events = %d", len(run.events))
	}
}
