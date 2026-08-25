package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (a *app) listBots(w http.ResponseWriter, _ *http.Request) {
	a.botsMu.RLock()
	items := make([]bot, len(a.bots))
	copy(items, a.bots)
	a.botsMu.RUnlock()
	writeJSON(w, http.StatusOK, items)
}

func (a *app) getBot(w http.ResponseWriter, r *http.Request) {
	item, ok := a.bot(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *app) addBot(w http.ResponseWriter, r *http.Request) {
	input, ok := a.readBot(w, r)
	if !ok {
		return
	}
	a.botsMu.Lock()
	defer a.botsMu.Unlock()
	for _, item := range a.bots {
		if item.WorkspaceRoot == input.WorkspaceRoot {
			http.Error(w, "workspace is already used by another bot", http.StatusConflict)
			return
		}
	}
	projects := make([]project, len(a.bots))
	for i, item := range a.bots {
		projects[i] = project{ID: item.ID}
	}
	id, err := projectID(input.Name, projects)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	input.ID = id
	a.bots = append(a.bots, input)
	if err := a.writeBots(); err != nil {
		a.bots = a.bots[:len(a.bots)-1]
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, input)
}

func (a *app) updateBot(w http.ResponseWriter, r *http.Request) {
	input, ok := a.readBot(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	a.botsMu.Lock()
	defer a.botsMu.Unlock()
	index := -1
	for i, item := range a.bots {
		if item.ID == id {
			index = i
			continue
		}
		if item.WorkspaceRoot == input.WorkspaceRoot {
			http.Error(w, "workspace is already used by another bot", http.StatusConflict)
			return
		}
	}
	if index == -1 {
		http.NotFound(w, r)
		return
	}
	previous := a.bots[index]
	input.ID = id
	a.bots[index] = input
	if err := a.writeBots(); err != nil {
		a.bots[index] = previous
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, input)
}

func (a *app) deleteBot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sessions, err := a.sessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, session := range sessions {
		if session.BotID == id {
			http.Error(w, "delete this bot's chats first", http.StatusConflict)
			return
		}
	}
	a.botsMu.Lock()
	defer a.botsMu.Unlock()
	index := -1
	for i, item := range a.bots {
		if item.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		http.NotFound(w, r)
		return
	}
	previous := append([]bot(nil), a.bots...)
	a.bots = append(a.bots[:index], a.bots[index+1:]...)
	if err := a.writeBots(); err != nil {
		a.bots = previous
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) readBot(w http.ResponseWriter, r *http.Request) (bot, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	var input bot
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid bot", http.StatusBadRequest)
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Model = strings.TrimSpace(input.Model)
	if input.Name == "" || len([]rune(input.Name)) > 80 || input.Prompt == "" {
		http.Error(w, "name and prompt are required", http.StatusBadRequest)
		return input, false
	}
	workspace, err := a.allowedPath(input.WorkspaceRoot)
	if err != nil {
		http.Error(w, "workspace is outside the allowed roots", http.StatusBadRequest)
		return input, false
	}
	input.WorkspaceRoot = workspace
	if input.SkillRoot != "" {
		input.SkillRoot, err = a.allowedPath(input.SkillRoot)
		if err != nil {
			http.Error(w, "skill root is outside the allowed roots", http.StatusBadRequest)
			return input, false
		}
	}
	allowed := make(map[string]struct{})
	for _, name := range strings.Fields(os.Getenv("AX_TOOLS")) {
		allowed[name] = struct{}{}
	}
	seen := make(map[string]struct{})
	tools := make([]string, 0, len(input.Tools))
	for _, name := range input.Tools {
		name = strings.TrimSpace(name)
		if _, ok := allowed[name]; !ok {
			http.Error(w, "tool is not allowed: "+name, http.StatusBadRequest)
			return input, false
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, name)
	}
	sort.Strings(tools)
	input.Tools = tools
	return input, true
}

func (a *app) bot(id string) (bot, bool) {
	a.botsMu.RLock()
	defer a.botsMu.RUnlock()
	for _, item := range a.bots {
		if item.ID == id {
			return item, true
		}
	}
	return bot{}, false
}

func loadBots(roots []root) (string, []bot, error) {
	path := os.Getenv("AXIS_BOTS_FILE")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".config", "axis", "bots.json")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var config botsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", nil, err
	}
	a := app{roots: roots}
	for i := range config.Bots {
		workspace, err := a.allowedPath(config.Bots[i].WorkspaceRoot)
		if err != nil {
			return "", nil, err
		}
		config.Bots[i].WorkspaceRoot = workspace
		if config.Bots[i].SkillRoot != "" {
			skillRoot, err := a.allowedPath(config.Bots[i].SkillRoot)
			if err != nil {
				return "", nil, err
			}
			config.Bots[i].SkillRoot = skillRoot
		}
	}
	return path, config.Bots, nil
}

func botEnvironmentDirectory() string {
	path := os.Getenv("AXIS_BOT_ENV_DIR")
	if path != "" {
		return path
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "axis", "bots")
}

func (a *app) readBotEnvironment(id string) ([]string, error) {
	return readEnvironmentFile(filepath.Join(a.botEnvDir, id+".env"))
}

func readEnvironmentFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("bot environment file must have mode 0600: %s", info.Name())
	}
	var environment []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !environmentKey(key) {
			return nil, fmt.Errorf("invalid bot environment line")
		}
		if strings.HasPrefix(value, `"`) {
			value, err = strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("invalid bot environment value for %s", key)
			}
		}
		environment = append(environment, key+"="+value)
	}
	return environment, scanner.Err()
}

func environmentKey(value string) bool {
	if value == "" || value[0] != '_' && (value[0] < 'A' || value[0] > 'Z') && (value[0] < 'a' || value[0] > 'z') {
		return false
	}
	for _, char := range value[1:] {
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func overlayEnvironment(base, values []string) []string {
	keys := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		keys[key] = struct{}{}
	}
	result := make([]string, 0, len(base)+len(values))
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if _, replaced := keys[key]; !replaced {
			result = append(result, value)
		}
	}
	return append(result, values...)
}

func botEnvironment(base []string, definition bot) []string {
	values := []string{
		"AX_TOOLS=" + strings.Join(definition.Tools, " "),
		"AX_WORKSPACE=" + definition.WorkspaceRoot,
		"FSX_WORKSPACE=" + definition.WorkspaceRoot,
		"BASHX_WORKSPACE=" + definition.WorkspaceRoot,
		"SKILLX_ROOT=" + definition.SkillRoot,
	}
	return overlayEnvironment(base, values)
}

func (a *app) writeBots() error {
	if err := os.MkdirAll(filepath.Dir(a.botsPath), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(botsConfig{Bots: a.bots}, "", "  ")
	if err != nil {
		return err
	}
	temporary := a.botsPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, a.botsPath)
}
