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
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yuin/goldmark"
)

type metadata struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type message struct {
	Role    string
	Content string
	HTML    template.HTML
}

type sessionView struct {
	Sessions []metadata
	Current  metadata
	Messages []message
}

type run struct {
	id       string
	session  string
	cancel   context.CancelFunc
	mu       sync.Mutex
	events   []string
	done     bool
	watchers map[chan string]struct{}
}

type app struct {
	root     string
	ax       string
	username string
	password string
	mu       sync.Mutex
	runs     map[string]*run
	active   map[string]string
}

var markdown = goldmark.New()
var page = template.Must(template.New("page").Parse(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Axo</title><style>
:root{color-scheme:dark;--bg:#090d16;--panel:#111827;--line:#263244;--muted:#93a4ba;--blue:#60a5fa}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:#e5edf8;font:15px system-ui}a{color:inherit;text-decoration:none}.layout{min-height:100vh;display:grid;grid-template-columns:260px 1fr}.side{border-right:1px solid var(--line);padding:16px;background:#0c1220}.brand{font-size:22px;font-weight:750}.new{display:block;background:var(--blue);color:#07111f;padding:10px 12px;border-radius:8px;text-align:center;margin:20px 0}.session{display:block;padding:10px;border-radius:7px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.session:hover{background:var(--panel)}main{max-width:850px;width:100%;margin:auto;padding:24px}.messages{padding-bottom:130px}.message{margin:24px 0}.role{color:var(--muted);font-size:12px;text-transform:uppercase}.content{line-height:1.6}.content pre{overflow:auto;background:#050810;padding:14px;border-radius:8px}.composer{position:fixed;bottom:0;left:260px;right:0;padding:16px;background:linear-gradient(transparent,var(--bg) 25%)}form.chat{max-width:850px;margin:auto;display:flex;gap:8px;background:var(--panel);border:1px solid var(--line);padding:8px;border-radius:12px}textarea{flex:1;resize:none;background:transparent;color:inherit;border:0;padding:8px;min-height:46px}button{border:0;border-radius:8px;padding:0 18px;background:var(--blue);color:#07111f;font-weight:700}.tools{color:var(--muted);font-size:13px}.mobile-head{display:none}@media(max-width:700px){.layout{display:block}.side{display:none}.mobile-head{display:flex;padding:14px 18px;border-bottom:1px solid var(--line);justify-content:space-between}.composer{left:0}main{padding:18px}}
</style></head><body>{{template "body" .}}</body></html>`))
var homePage = template.Must(template.Must(page.Clone()).New("body").Parse(`<div class="mobile-head"><b>Axo</b><a href="/">Sessions</a></div><div class="layout"><aside class="side"><div class="brand">Axo</div><form method="post" action="/sessions"><button class="new">New chat</button></form>{{range .Sessions}}<a class="session" href="/sessions/{{.ID}}">{{.Title}}</a>{{end}}</aside><main>{{if .Current.ID}}<div class="messages" id="messages">{{range .Messages}}<div class="message"><div class="role">{{.Role}}</div><div class="content">{{.HTML}}</div></div>{{end}}<div id="live"></div></div><div class="composer"><form class="chat" id="chat"><textarea name="prompt" placeholder="Message AX" required></textarea><button>Send</button></form></div>{{else}}<h1>Start a conversation</h1><form method="post" action="/sessions"><button class="new">New chat</button></form>{{end}}</main></div>{{if .Current.ID}}<script>
const form=document.querySelector('#chat'),live=document.querySelector('#live');form.addEventListener('submit',async e=>{e.preventDefault();const prompt=form.prompt.value.trim();if(!prompt)return;form.querySelector('button').disabled=true;live.innerHTML='<div class="message"><div class="role">You</div><div class="content"></div></div><div class="message"><div class="role">AX</div><div class="content" id="answer"></div><div class="tools" id="tools"></div></div>';live.querySelector('.message .content').textContent=prompt;const response=await fetch('/sessions/{{.Current.ID}}/messages',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams({prompt})});if(!response.ok){live.querySelector('#answer').textContent=await response.text();return}const data=await response.json(),source=new EventSource('/runs/'+data.run_id+'/events'),answer=live.querySelector('#answer'),tools=live.querySelector('#tools');source.addEventListener('delta',e=>{answer.textContent+=JSON.parse(e.data);scrollTo(0,document.body.scrollHeight)});source.addEventListener('tool',e=>tools.textContent=JSON.parse(e.data));source.addEventListener('done',()=>{source.close();location.reload()});source.addEventListener('failure',e=>{answer.textContent=JSON.parse(e.data);source.close();form.querySelector('button').disabled=false});form.prompt.value=''});
</script>{{end}}`))

func main() {
	root := os.Getenv("AXO_DATA_DIR")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".local", "share", "axo")
	}
	ax := os.Getenv("AXO_AX_PATH")
	if ax == "" {
		ax = "ax"
	}
	a := &app{root: root, ax: ax, username: os.Getenv("AXO_USERNAME"), password: os.Getenv("AXO_PASSWORD"), runs: make(map[string]*run), active: make(map[string]string)}
	if a.password != "" && a.username == "" {
		a.username = "axbot"
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.home)
	mux.HandleFunc("POST /sessions", a.createSession)
	mux.HandleFunc("GET /sessions/{id}", a.showSession)
	mux.HandleFunc("POST /sessions/{id}/messages", a.sendMessage)
	mux.HandleFunc("GET /runs/{id}/events", a.streamRun)
	mux.HandleFunc("POST /runs/{id}/cancel", a.cancelRun)
	address := os.Getenv("AXO_ADDRESS")
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
				w.Header().Set("WWW-Authenticate", `Basic realm="Axo"`)
				http.Error(w, "authentication required", 401)
				return
			}
		}
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				http.Error(w, "invalid origin", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) home(w http.ResponseWriter, r *http.Request) {
	sessions, _ := a.sessions()
	homePage.Execute(w, sessionView{Sessions: sessions})
}

func (a *app) createSession(w http.ResponseWriter, r *http.Request) {
	id, err := randomID()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now().UTC()
	item := metadata{ID: id, Title: "New chat", CreatedAt: now, UpdatedAt: now}
	if err := a.writeMetadata(item); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/sessions/"+id, 303)
}

func (a *app) showSession(w http.ResponseWriter, r *http.Request) {
	item, err := a.readMetadata(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sessions, _ := a.sessions()
	messages, _ := a.readMessages(item.ID)
	homePage.Execute(w, sessionView{Sessions: sessions, Current: item, Messages: messages})
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
	run := &run{id: id, session: item.ID, cancel: cancel, watchers: make(map[chan string]struct{})}
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
	cmd := exec.CommandContext(ctx, a.ax, "--events", "--session", filepath.Join(path, "session.jsonl"), prompt)
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
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "assistant_delta":
			run.publish("delta", event.Text)
		case "tool_start":
			run.publish("tool", "Using "+event.Name+"…")
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
	item, err := a.readMetadata(run.session)
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
	data, _ := json.Marshal(value)
	event := "event: " + kind + "\ndata: " + string(data) + "\n\n"
	run.mu.Lock()
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
	run.terminate("event: done\ndata: null\n\n")
}

func (run *run) fail(value string) {
	data, _ := json.Marshal(value)
	run.terminate("event: failure\ndata: " + string(data) + "\n\n")
}

func (run *run) terminate(event string) {
	run.mu.Lock()
	run.done = true
	run.events = append(run.events, event)
	for watcher := range run.watchers {
		watcher <- event
		close(watcher)
		delete(run.watchers, watcher)
	}
	run.mu.Unlock()
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
	watcher := make(chan string, 32)
	run.mu.Lock()
	past := append([]string(nil), run.events...)
	if !run.done {
		run.watchers[watcher] = struct{}{}
	}
	done := run.done
	run.mu.Unlock()
	for _, event := range past {
		io.WriteString(w, event)
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
			io.WriteString(w, event)
			flusher.Flush()
			if strings.HasPrefix(event, "event: done") || strings.HasPrefix(event, "event: failure") {
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
			Type    string  `json:"type"`
			Message message `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "message" || entry.Message.Content == "" {
			continue
		}
		var rendered strings.Builder
		markdown.Convert([]byte(entry.Message.Content), &rendered)
		entry.Message.HTML = template.HTML(rendered.String())
		messages = append(messages, entry.Message)
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

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
