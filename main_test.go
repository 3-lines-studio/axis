package main

import (
	"context"
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
func TestDeleteProject(t *testing.T) {
	root := t.TempDir()
	a := app{
		root:         root,
		projectsPath: filepath.Join(root, "projects.json"),
		projects:     []project{{ID: "test", Name: "Test", Path: root}},
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/projects/test", nil)
	request.SetPathValue("id", "test")
	response := httptest.NewRecorder()
	a.deleteProject(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if len(a.projects) != 0 {
		t.Fatalf("projects = %d", len(a.projects))
	}
}

func TestDeleteProjectRejectsProjectWithSessions(t *testing.T) {
	root := t.TempDir()
	a := app{
		root:         root,
		projectsPath: filepath.Join(root, "projects.json"),
		projects:     []project{{ID: "test", Name: "Test", Path: root}},
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := a.writeMetadata(metadata{ID: strings.Repeat("c", 24), ProjectID: "test", Title: "Chat", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/projects/test", nil)
	request.SetPathValue("id", "test")
	response := httptest.NewRecorder()
	a.deleteProject(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d", response.Code)
	}
	if len(a.projects) != 1 {
		t.Fatalf("projects = %d", len(a.projects))
	}
}

func TestBotAPI(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	skills := filepath.Join(base, "skills")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AX_TOOLS", "fsx skillx")
	a := app{roots: []root{{Name: "Bots", Path: base}}, botsPath: filepath.Join(base, "bots.json")}
	body := `{"name":"Alfred","prompt":"Be useful","tools":["skillx","fsx"],"workspace_root":"` + workspace + `","skill_root":"` + skills + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/bots", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.addBot(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if len(a.bots) != 1 || a.bots[0].ID != "alfred" || strings.Join(a.bots[0].Tools, " ") != "fsx skillx" {
		t.Fatalf("bots = %#v", a.bots)
	}
}

func TestBotRejectsUnknownTool(t *testing.T) {
	base := t.TempDir()
	t.Setenv("AX_TOOLS", "fsx")
	a := app{roots: []root{{Name: "Bots", Path: base}}, botsPath: filepath.Join(base, "bots.json")}
	body := `{"name":"Alfred","prompt":"Be useful","tools":["bashx"],"workspace_root":"` + base + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/bots", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.addBot(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestReadBotEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "alfred.env")
	if err := os.WriteFile(path, []byte("DATABASE_URL=postgres://localhost/test\nTOKEN=\"quoted value\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := app{botEnvDir: directory}
	environment, err := a.readBotEnvironment("alfred")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(environment, "\n") != "DATABASE_URL=postgres://localhost/test\nTOKEN=quoted value" {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestReadBotEnvironmentRejectsOpenPermissions(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "alfred.env"), []byte("TOKEN=value\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a := app{botEnvDir: directory}
	if _, err := a.readBotEnvironment("alfred"); err == nil {
		t.Fatal("accepted open permissions")
	}
}

func TestBotEnvironmentOverridesSecretsAndProtectsWorkspace(t *testing.T) {
	base := []string{"DATABASE_URL=old", "AX_TOOLS=wrong"}
	secrets := []string{"DATABASE_URL=new", "AX_TOOLS=secret"}
	definition := bot{Tools: []string{"fsx"}, WorkspaceRoot: "/workspace", SkillRoot: "/skills"}
	environment := botEnvironment(overlayEnvironment(base, secrets), definition)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "DATABASE_URL=old") || !strings.Contains(joined, "DATABASE_URL=new") || !strings.Contains(joined, "AX_TOOLS=fsx") || strings.Contains(joined, "AX_TOOLS=secret") {
		t.Fatalf("environment = %q", joined)
	}
}

func TestConnectorAPI(t *testing.T) {
	base := t.TempDir()
	a := app{
		root:             base,
		bots:             []bot{{ID: "alfred", Name: "Alfred"}},
		projects:         []project{{ID: "alfred", Name: "Alfred", Path: base}},
		connectorsPath:   filepath.Join(base, "connectors.json"),
		connectorCancels: make(map[string]context.CancelFunc),
	}
	body := `{"name":"Alfred Slack","type":"slack","bot_id":"alfred","project_id":"alfred","enabled":false}`
	request := httptest.NewRequest(http.MethodPost, "/api/connectors", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.addConnector(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if len(a.connectors) != 1 || a.connectors[0].ID != "alfred-slack" {
		t.Fatalf("connectors = %#v", a.connectors)
	}
}

func TestCancelRunEndsGracefully(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	session := strings.Repeat("c", 24)
	if err := os.MkdirAll(filepath.Join(base, "sessions", session), 0700); err != nil {
		t.Fatal(err)
	}
	axPath := filepath.Join(base, "ax.sh")
	script := `#!/bin/sh
printf '%s\n' '{"type":"protocol","version":1}'
while IFS= read -r line; do
  printf '%s\n' "$line" > "` + base + `/stdin.txt"
  printf '%s\n' '{"type":"done","outcome":"cancelled"}'
  exit 0
done
`
	if err := os.WriteFile(axPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	a := app{
		root:   base,
		ax:     axPath,
		runs:   make(map[string]*run),
		active: map[string]string{},
	}
	now := time.Now().UTC()
	if err := a.writeMetadata(metadata{ID: session, ProjectID: "p", Title: "T", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a.projects = []project{{ID: "p", Name: "P", Path: base}}
	a.active = make(map[string]string)
	a.runs = make(map[string]*run)
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &run{id: "r1", session: session, cancel: cancel, status: "running", watchers: make(map[chan runEvent]struct{})}
	a.runs["r1"] = run
	a.active[session] = "r1"
	go func() {
		a.execute(ctx, run, "hello")
		close(finished)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run.mu.Lock()
		gracefulSet := run.graceful != nil
		run.mu.Unlock()
		if gracefulSet {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	request := httptest.NewRequest(http.MethodPost, "/runs/r1/cancel", nil)
	request.SetPathValue("id", "r1")
	response := httptest.NewRecorder()
	a.cancelRun(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d", response.Code)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish after cancel")
	}
	data, err := os.ReadFile(filepath.Join(base, "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != `{"type":"cancel"}` {
		t.Fatalf("stdin = %q", string(data))
	}
	run.mu.Lock()
	status := run.status
	run.mu.Unlock()
	if status != "done" {
		t.Fatalf("status = %q", status)
	}
}
