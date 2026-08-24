package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	run := &run{watchers: make(map[chan runEvent]struct{})}
	run.publish("delta", "hello")
	run.finish()
	if len(run.events) != 2 {
		t.Fatalf("events = %d", len(run.events))
	}
}

func TestReadMessagesIncludesToolCalls(t *testing.T) {
	a := app{root: t.TempDir()}
	id := strings.Repeat("b", 24)
	path, err := a.sessionPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	data := "{\"type\":\"message\",\"message\":{\"Role\":\"assistant\",\"Content\":\"\",\"ToolCalls\":[{\"ID\":\"call-1\",\"Name\":\"read\",\"Arguments\":\"{\\\"path\\\":\\\"main.go\\\"}\"}]}}\n" +
		"{\"type\":\"message\",\"message\":{\"Role\":\"tool\",\"Content\":\"contents\",\"ToolCallID\":\"call-1\"}}\n"
	if err := os.WriteFile(filepath.Join(path, "session.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	messages, err := a.readMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].Name != "read" || messages[1].ToolCallID != "call-1" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestAllowedPathRejectsSibling(t *testing.T) {
	parent := t.TempDir()
	allowed := filepath.Join(parent, "allowed")
	sibling := filepath.Join(parent, "sibling")
	if err := os.MkdirAll(allowed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	a := app{roots: []root{{Name: "Allowed", Path: allowed}}}
	if _, err := a.allowedPath(sibling); err == nil {
		t.Fatal("allowed sibling directory")
	}
}

func TestProjectSessionAPI(t *testing.T) {
	path := t.TempDir()
	a := app{root: t.TempDir(), projects: []project{{ID: "test", Name: "Test", Path: path}}}
	if err := os.MkdirAll(filepath.Join(a.root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/test/sessions", nil)
	request.SetPathValue("id", "test")
	response := httptest.NewRecorder()
	a.createProjectSession(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d", response.Code)
	}
	var created metadata
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ProjectID != "test" {
		t.Fatalf("project = %q", created.ProjectID)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/projects/test/sessions", nil)
	request.SetPathValue("id", "test")
	response = httptest.NewRecorder()
	a.listProjectSessions(response, request)
	var sessions []metadata
	if err := json.NewDecoder(response.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/sessions/"+created.ID, nil)
	request.SetPathValue("id", created.ID)
	response = httptest.NewRecorder()
	a.deleteSession(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.Code)
	}
	if _, err := a.readMetadata(created.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata still exists: %v", err)
	}
}
