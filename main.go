package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type root struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type projectsConfig struct {
	Roots    []root    `json:"roots"`
	Projects []project `json:"projects"`
}

type directory struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind,omitempty"`
	Registered bool   `json:"registered"`
}

type directoryResponse struct {
	Path        string      `json:"path,omitempty"`
	Parent      string      `json:"parent,omitempty"`
	Roots       []root      `json:"roots,omitempty"`
	Directories []directory `json:"directories,omitempty"`
}

type metadata struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type storedToolCall struct {
	ID        string `json:"ID"`
	Name      string `json:"Name"`
	Arguments string `json:"Arguments"`
}

type storedMessage struct {
	Role       string           `json:"Role"`
	Content    string           `json:"Content"`
	ToolCalls  []storedToolCall `json:"ToolCalls"`
	ToolCallID string           `json:"ToolCallID"`
}

type sessionResponse struct {
	metadata
	Messages []message `json:"messages"`
}

type runEvent struct {
	sequence int
	data     string
}

type runSummary struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type run struct {
	id       string
	session  string
	cancel   context.CancelFunc
	mu       sync.Mutex
	events   []runEvent
	sequence int
	status   string
	done     bool
	watchers map[chan runEvent]struct{}
}

type app struct {
	root         string
	ax           string
	username     string
	password     string
	projectsPath string
	roots        []root
	projects     []project
	projectsMu   sync.RWMutex
	mu           sync.Mutex
	runs         map[string]*run
	active       map[string]string
}

func main() {
	root := os.Getenv("AXIS_DATA_DIR")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".local", "share", "axis")
	}
	ax := os.Getenv("AXIS_AX_PATH")
	if ax == "" {
		ax = "ax"
	}
	projectsPath, roots, projects, err := loadProjects()
	if err != nil {
		log.Fatal(err)
	}
	a := &app{
		root:         root,
		ax:           ax,
		username:     os.Getenv("AXIS_USERNAME"),
		password:     os.Getenv("AXIS_PASSWORD"),
		projectsPath: projectsPath,
		roots:        roots,
		projects:     projects,
		runs:         make(map[string]*run),
		active:       make(map[string]string),
	}
	if a.password != "" && a.username == "" {
		a.username = "axbot"
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", a.listProjects)
	mux.HandleFunc("POST /api/projects", a.addProject)
	mux.HandleFunc("GET /api/directories", a.listDirectories)
	mux.HandleFunc("GET /api/projects/{id}/sessions", a.listProjectSessions)
	mux.HandleFunc("POST /api/projects/{id}/sessions", a.createProjectSession)
	mux.HandleFunc("GET /api/sessions/{id}", a.getSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", a.deleteSession)
	mux.HandleFunc("POST /sessions/{id}/messages", a.sendMessage)
	mux.HandleFunc("GET /api/runs", a.listRuns)
	mux.HandleFunc("GET /runs/{id}/events", a.streamRun)
	mux.HandleFunc("POST /runs/{id}/cancel", a.cancelRun)
	address := os.Getenv("AXIS_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	server := &http.Server{Addr: address, Handler: a.secure(mux)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		a.cancelRuns()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("listening on %s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *app) cancelRuns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, run := range a.runs {
		run.mu.Lock()
		done := run.done
		run.mu.Unlock()
		if !done {
			run.cancel()
		}
	}
}

func (a *app) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.password != "" {
			user, password, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(a.username)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Axis"`)
				http.Error(w, "authentication required", 401)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				http.Error(w, "invalid origin", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) listProjects(w http.ResponseWriter, _ *http.Request) {
	a.projectsMu.RLock()
	items := make([]project, len(a.projects))
	for index, item := range a.projects {
		items[index] = project{ID: item.ID, Name: item.Name}
	}
	a.projectsMu.RUnlock()
	writeJSON(w, http.StatusOK, items)
}

func (a *app) addProject(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var input project
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid project", http.StatusBadRequest)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	path, err := a.allowedPath(input.Path)
	if err != nil || input.Name == "" || len([]rune(input.Name)) > 80 {
		http.Error(w, "invalid project name or directory", http.StatusBadRequest)
		return
	}

	a.projectsMu.Lock()
	defer a.projectsMu.Unlock()
	for _, item := range a.projects {
		if item.Path == path {
			writeJSON(w, http.StatusOK, project{ID: item.ID, Name: item.Name})
			return
		}
	}
	id, err := projectID(input.Name, a.projects)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	item := project{ID: id, Name: input.Name, Path: path}
	a.projects = append(a.projects, item)
	if err := a.writeProjects(); err != nil {
		a.projects = a.projects[:len(a.projects)-1]
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, project{ID: item.ID, Name: item.Name})
}

func (a *app) listDirectories(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	if requested == "" {
		writeJSON(w, http.StatusOK, directoryResponse{Roots: a.roots})
		return
	}
	path, err := a.allowedPath(requested)
	if err != nil {
		http.Error(w, "directory is outside the allowed roots", http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	directories := make([]directory, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		child := filepath.Join(path, entry.Name())
		resolved, err := a.allowedPath(child)
		if err != nil {
			continue
		}
		directories = append(directories, directory{Name: entry.Name(), Path: resolved, Kind: projectKind(resolved), Registered: a.registeredPath(resolved)})
	}
	parent := ""
	candidate := filepath.Dir(path)
	if candidate != path {
		if resolved, err := a.allowedPath(candidate); err == nil {
			parent = resolved
		}
	}
	writeJSON(w, http.StatusOK, directoryResponse{Path: path, Parent: parent, Directories: directories})
}

func (a *app) listProjectSessions(w http.ResponseWriter, r *http.Request) {
	item, ok := a.project(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sessions, err := a.projectSessions(item.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (a *app) createProjectSession(w http.ResponseWriter, r *http.Request) {
	item, ok := a.project(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	session, err := a.newSession(item.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (a *app) getSession(w http.ResponseWriter, r *http.Request) {
	item, err := a.readMetadata(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	messages, err := a.readMessages(item.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{metadata: item, Messages: messages})
}

func (a *app) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, err := a.sessionPath(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := a.readMetadata(id); err != nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	_, active := a.active[id]
	a.mu.Unlock()
	if active {
		http.Error(w, "session is running", http.StatusConflict)
		return
	}
	if err := os.RemoveAll(path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) newSession(projectID string) (metadata, error) {
	id, err := randomID()
	if err != nil {
		return metadata{}, err
	}
	now := time.Now().UTC()
	item := metadata{ID: id, ProjectID: projectID, Title: "New chat", CreatedAt: now, UpdatedAt: now}
	return item, a.writeMetadata(item)
}

func (a *app) sendMessage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" || len(prompt) > 10000 {
		http.Error(w, "prompt must contain 1 to 10000 bytes", 400)
		return
	}
	item, err := a.readMetadata(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	if _, exists := a.active[item.ID]; exists {
		a.mu.Unlock()
		http.Error(w, "session is running", 409)
		return
	}
	id, err := randomID()
	if err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), 500)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &run{id: id, session: item.ID, cancel: cancel, status: "running", watchers: make(map[chan runEvent]struct{})}
	a.runs[id] = run
	a.active[item.ID] = id
	a.mu.Unlock()
	go a.execute(ctx, run, prompt)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"run_id": id})
}

func (a *app) execute(ctx context.Context, run *run, prompt string) {
	defer func() {
		a.mu.Lock()
		delete(a.active, run.session)
		a.mu.Unlock()
	}()
	path, _ := a.sessionPath(run.session)
	item, err := a.readMetadata(run.session)
	if err != nil {
		run.fail(err.Error())
		return
	}
	project, ok := a.project(item.ProjectID)
	if !ok {
		run.fail("project not found")
		return
	}
	cmd := exec.CommandContext(ctx, a.ax, "-C", project.Path, "--events", "--session", filepath.Join(path, "session.jsonl"), prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		run.fail(err.Error())
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		run.fail(err.Error())
		return
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Text      string `json:"text"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "assistant_delta":
			run.publish("delta", event.Text)
		case "tool_start":
			run.publish("tool", "Using "+event.Name+"…")
			run.publishValue("tool_start", toolCall{ID: event.ID, Name: event.Name, Arguments: event.Arguments})
		case "tool_delta":
			run.publishValue("tool_delta", map[string]string{"id": event.ID, "text": event.Text})
		case "tool_result":
			run.publishValue("tool_result", map[string]string{"id": event.ID, "output": event.Output})
		case "tool_done":
			run.publishValue("tool_done", map[string]string{"id": event.ID})
		}
	}
	err = cmd.Wait()
	if err != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			text = err.Error()
		}
		run.fail(text)
		return
	}
	item, err = a.readMetadata(run.session)
	if err == nil {
		if item.Title == "New chat" {
			item.Title = prompt
			if len([]rune(item.Title)) > 48 {
				item.Title = string([]rune(item.Title)[:48]) + "…"
			}
		}
		item.UpdatedAt = time.Now().UTC()
		a.writeMetadata(item)
	}
	run.finish()
}

func (run *run) publish(kind, value string) {
	run.publishValue(kind, value)
}

func (run *run) publishValue(kind string, value any) {
	data, _ := json.Marshal(value)
	run.mu.Lock()
	run.sequence++
	event := runEvent{sequence: run.sequence, data: "event: " + kind + "\ndata: " + string(data) + "\n\n"}
	run.events = append(run.events, event)
	for watcher := range run.watchers {
		select {
		case watcher <- event:
		default:
		}
	}
	run.mu.Unlock()
}

func (run *run) finish() {
	run.terminate("done", "done", nil)
}

func (run *run) fail(value string) {
	run.terminate("failure", "failed", value)
}

func (run *run) terminate(kind, status string, value any) {
	data, _ := json.Marshal(value)
	run.mu.Lock()
	run.sequence++
	event := runEvent{sequence: run.sequence, data: "event: " + kind + "\ndata: " + string(data) + "\n\n"}
	run.done = true
	run.status = status
	run.events = append(run.events, event)
	for watcher := range run.watchers {
		watcher <- event
		close(watcher)
		delete(run.watchers, watcher)
	}
	run.mu.Unlock()
}

func (a *app) listRuns(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	items := make([]runSummary, 0, len(a.active))
	for sessionID, runID := range a.active {
		run := a.runs[runID]
		run.mu.Lock()
		status := run.status
		run.mu.Unlock()
		items = append(items, runSummary{ID: runID, SessionID: sessionID, Status: status})
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, items)
}

func writeRunEvent(w io.Writer, event runEvent) {
	fmt.Fprintf(w, "id: %d\n%s", event.sequence, event.data)
}

func (a *app) streamRun(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	run := a.runs[r.PathValue("id")]
	a.mu.Unlock()
	if run == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	watcher := make(chan runEvent, 32)
	run.mu.Lock()
	past := make([]runEvent, 0, len(run.events))
	for _, event := range run.events {
		if event.sequence > after {
			past = append(past, event)
		}
	}
	if !run.done {
		run.watchers[watcher] = struct{}{}
	}
	done := run.done
	run.mu.Unlock()
	for _, event := range past {
		writeRunEvent(w, event)
	}
	flusher.Flush()
	if done {
		return
	}
	defer func() {
		run.mu.Lock()
		delete(run.watchers, watcher)
		run.mu.Unlock()
	}()
	for {
		select {
		case event, ok := <-watcher:
			if !ok {
				return
			}
			writeRunEvent(w, event)
			flusher.Flush()
			if strings.HasPrefix(event.data, "event: done") || strings.HasPrefix(event.data, "event: failure") {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (a *app) cancelRun(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	run := a.runs[r.PathValue("id")]
	a.mu.Unlock()
	if run == nil {
		http.NotFound(w, r)
		return
	}
	run.cancel()
	w.WriteHeader(204)
}

func (a *app) projectSessions(projectID string) ([]metadata, error) {
	items, err := a.sessions()
	if err != nil {
		return nil, err
	}
	filtered := make([]metadata, 0, len(items))
	for _, item := range items {
		if item.ProjectID == projectID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (a *app) sessions() ([]metadata, error) {
	entries, err := os.ReadDir(filepath.Join(a.root, "sessions"))
	if err != nil {
		return nil, err
	}
	items := make([]metadata, 0, len(entries))
	for _, entry := range entries {
		item, err := a.readMetadata(entry.Name())
		if err == nil {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	return items, nil
}

func (a *app) readMessages(id string) ([]message, error) {
	path, err := a.sessionPath(id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(path, "session.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var messages []message
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			Type    string        `json:"type"`
			Message storedMessage `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "message" {
			continue
		}
		item := message{Role: entry.Message.Role, Content: entry.Message.Content, ToolCallID: entry.Message.ToolCallID}
		for _, call := range entry.Message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, toolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		messages = append(messages, item)
	}
	return messages, scanner.Err()
}

func (a *app) sessionPath(id string) (string, error) {
	if len(id) != 24 || strings.ContainsAny(id, `/\\`) {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(a.root, "sessions", id), nil
}

func (a *app) readMetadata(id string) (metadata, error) {
	var item metadata
	path, err := a.sessionPath(id)
	if err != nil {
		return item, err
	}
	data, err := os.ReadFile(filepath.Join(path, "metadata.json"))
	if err != nil {
		return item, err
	}
	err = json.Unmarshal(data, &item)
	if err == nil && item.ProjectID == "" {
		if project, ok := a.project(""); ok {
			item.ProjectID = project.ID
		}
	}
	return item, err
}

func (a *app) writeMetadata(item metadata) error {
	path, err := a.sessionPath(item.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(path, "metadata.json.tmp")
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(path, "metadata.json"))
}

func loadProjects() (string, []root, []project, error) {
	configPath := os.Getenv("AXIS_PROJECTS_FILE")
	if configPath == "" {
		configPath = filepath.Join(os.Getenv("HOME"), ".config", "axis", "projects.json")
	}
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		projectPath := os.Getenv("AXIS_PROJECT_PATH")
		if projectPath == "" {
			projectPath, err = os.Getwd()
			if err != nil {
				return "", nil, nil, err
			}
		}
		browseRoot := os.Getenv("HOME")
		if browseRoot == "" {
			browseRoot = filepath.Dir(projectPath)
		}
		roots, err := validateRoots([]root{{Name: "Home", Path: browseRoot}})
		if err != nil {
			return "", nil, nil, err
		}
		projects, err := validateProjects([]project{{ID: "default", Name: filepath.Base(projectPath), Path: projectPath}})
		return configPath, roots, projects, err
	}
	if err != nil {
		return "", nil, nil, err
	}
	var config projectsConfig
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &config.Projects); err != nil {
			return "", nil, nil, fmt.Errorf("read projects: %w", err)
		}
		for _, item := range config.Projects {
			config.Roots = append(config.Roots, root{Name: item.Name, Path: filepath.Dir(item.Path)})
		}
	} else if err := json.Unmarshal(data, &config); err != nil {
		return "", nil, nil, fmt.Errorf("read projects: %w", err)
	}
	roots, err := validateRoots(config.Roots)
	if err != nil {
		return "", nil, nil, err
	}
	projects, err := validateProjects(config.Projects)
	return configPath, roots, projects, err
}

func validateRoots(items []root) ([]root, error) {
	if len(items) == 0 {
		return nil, errors.New("no directory roots configured")
	}
	paths := make(map[string]struct{}, len(items))
	result := make([]root, 0, len(items))
	for index := range items {
		item := items[index]
		if strings.TrimSpace(item.Name) == "" {
			return nil, fmt.Errorf("invalid root at index %d", index)
		}
		path, err := canonicalDirectory(item.Path)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", item.Name, err)
		}
		if _, exists := paths[path]; exists {
			continue
		}
		item.Path = path
		paths[path] = struct{}{}
		result = append(result, item)
	}
	return result, nil
}

func validateProjects(items []project) ([]project, error) {
	ids := make(map[string]struct{}, len(items))
	paths := make(map[string]struct{}, len(items))
	for index := range items {
		item := &items[index]
		if item.ID == "" || strings.ContainsAny(item.ID, `/\\`) || item.Name == "" || item.Path == "" {
			return nil, fmt.Errorf("invalid project at index %d", index)
		}
		if _, exists := ids[item.ID]; exists {
			return nil, fmt.Errorf("duplicate project ID %q", item.ID)
		}
		path, err := canonicalDirectory(item.Path)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", item.ID, err)
		}
		if _, exists := paths[path]; exists {
			return nil, fmt.Errorf("duplicate project path %q", path)
		}
		item.Path = path
		ids[item.ID] = struct{}{}
		paths[path] = struct{}{}
	}
	return items, nil
}

func canonicalDirectory(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return path, nil
}

func (a *app) project(id string) (project, bool) {
	a.projectsMu.RLock()
	defer a.projectsMu.RUnlock()
	if id == "" && len(a.projects) > 0 {
		return a.projects[0], true
	}
	for _, item := range a.projects {
		if item.ID == id {
			return item, true
		}
	}
	return project{}, false
}

func (a *app) allowedPath(path string) (string, error) {
	path, err := canonicalDirectory(path)
	if err != nil {
		return "", err
	}
	for _, item := range a.roots {
		relative, err := filepath.Rel(item.Path, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return path, nil
		}
	}
	return "", errors.New("path is outside the allowed roots")
}

func (a *app) registeredPath(path string) bool {
	a.projectsMu.RLock()
	defer a.projectsMu.RUnlock()
	for _, item := range a.projects {
		if item.Path == path {
			return true
		}
	}
	return false
}

func (a *app) writeProjects() error {
	data, err := json.MarshalIndent(projectsConfig{Roots: a.roots, Projects: a.projects}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.projectsPath), 0700); err != nil {
		return err
	}
	temporary := a.projectsPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, a.projectsPath)
}

func projectID(name string, projects []project) (string, error) {
	base := strings.Trim(strings.Map(func(value rune) rune {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
			return value
		}
		if value >= 'A' && value <= 'Z' {
			return value + ('a' - 'A')
		}
		return '-'
	}, name), "-")
	if base == "" {
		base = "project"
	}
	for _, item := range projects {
		if item.ID == base {
			suffix := make([]byte, 3)
			if _, err := rand.Read(suffix); err != nil {
				return "", err
			}
			return base + "-" + hex.EncodeToString(suffix), nil
		}
	}
	return base, nil
}

func projectKind(path string) string {
	markers := []struct {
		name string
		kind string
	}{
		{".git", "Git repository"},
		{"go.mod", "Go project"},
		{"Cargo.toml", "Rust project"},
		{"package.json", "Node project"},
		{"pyproject.toml", "Python project"},
	}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(path, marker.name)); err == nil {
			return marker.kind
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
