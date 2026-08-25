package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func connectorEnvironmentDirectory() string {
	if path := os.Getenv("AXIS_CONNECTOR_ENV_DIR"); path != "" {
		return path
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "axis", "connectors")
}

func loadConnectors() (string, []connector, error) {
	path := os.Getenv("AXIS_CONNECTORS_FILE")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".config", "axis", "connectors.json")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var config connectorsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", nil, err
	}
	return path, config.Connectors, nil
}

func (a *app) listConnectors(w http.ResponseWriter, _ *http.Request) {
	a.connectorsMu.Lock()
	items := append([]connector(nil), a.connectors...)
	a.connectorsMu.Unlock()
	writeJSON(w, http.StatusOK, items)
}

func (a *app) addConnector(w http.ResponseWriter, r *http.Request) {
	input, ok := a.readConnector(w, r)
	if !ok {
		return
	}
	a.connectorsMu.Lock()
	projects := make([]project, len(a.connectors))
	for i, item := range a.connectors {
		projects[i] = project{ID: item.ID}
	}
	id, err := projectID(input.Name, projects)
	if err != nil {
		a.connectorsMu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	input.ID = id
	input.Status = "stopped"
	a.connectors = append(a.connectors, input)
	if err := a.writeConnectors(); err != nil {
		a.connectors = a.connectors[:len(a.connectors)-1]
		a.connectorsMu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.connectorsMu.Unlock()
	a.reconcileConnector(id)
	writeJSON(w, http.StatusCreated, input)
}

func (a *app) updateConnector(w http.ResponseWriter, r *http.Request) {
	input, ok := a.readConnector(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	a.connectorsMu.Lock()
	index := a.connectorIndex(id)
	if index == -1 {
		a.connectorsMu.Unlock()
		http.NotFound(w, r)
		return
	}
	previous := a.connectors[index]
	input.ID = id
	input.Status = previous.Status
	input.Restarts = previous.Restarts
	a.connectors[index] = input
	if err := a.writeConnectors(); err != nil {
		a.connectors[index] = previous
		a.connectorsMu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.connectorsMu.Unlock()
	a.reconcileConnector(id)
	writeJSON(w, http.StatusOK, input)
}

func (a *app) deleteConnector(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.connectorsMu.Lock()
	index := a.connectorIndex(id)
	if index == -1 {
		a.connectorsMu.Unlock()
		http.NotFound(w, r)
		return
	}
	previous := append([]connector(nil), a.connectors...)
	a.connectors = append(a.connectors[:index], a.connectors[index+1:]...)
	if cancel := a.connectorCancels[id]; cancel != nil {
		cancel()
		delete(a.connectorCancels, id)
	}
	if err := a.writeConnectors(); err != nil {
		a.connectors = previous
		a.connectorsMu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.connectorsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) readConnector(w http.ResponseWriter, r *http.Request) (connector, bool) {
	var input connector
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid connector", http.StatusBadRequest)
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || input.Type != "slack" {
		http.Error(w, "name and supported type are required", http.StatusBadRequest)
		return input, false
	}
	if _, ok := a.bot(input.BotID); !ok {
		http.Error(w, "bot not found", http.StatusBadRequest)
		return input, false
	}
	if _, ok := a.project(input.ProjectID); !ok {
		http.Error(w, "project not found", http.StatusBadRequest)
		return input, false
	}
	return input, true
}

func (a *app) startConnectors(ctx context.Context) {
	a.connectorsMu.Lock()
	ids := make([]string, 0, len(a.connectors))
	for _, item := range a.connectors {
		if item.Enabled {
			ids = append(ids, item.ID)
		}
	}
	a.connectorsMu.Unlock()
	for _, id := range ids {
		a.startConnector(ctx, id)
	}
}

func (a *app) reconcileConnector(id string) {
	a.connectorsMu.Lock()
	index := a.connectorIndex(id)
	if index == -1 {
		a.connectorsMu.Unlock()
		return
	}
	enabled := a.connectors[index].Enabled
	cancel := a.connectorCancels[id]
	if cancel != nil {
		cancel()
		delete(a.connectorCancels, id)
	}
	a.connectorsMu.Unlock()
	if enabled {
		parent := a.connectorCtx
		if parent == nil {
			parent = context.Background()
		}
		a.startConnector(parent, id)
	}
}

func (a *app) startConnector(parent context.Context, id string) {
	ctx, cancel := context.WithCancel(parent)
	a.connectorsMu.Lock()
	if _, exists := a.connectorCancels[id]; exists {
		a.connectorsMu.Unlock()
		cancel()
		return
	}
	a.connectorCancels[id] = cancel
	a.connectorsMu.Unlock()
	go a.superviseConnector(ctx, id)
}

func (a *app) superviseConnector(ctx context.Context, id string) {
	delay := time.Second
	for ctx.Err() == nil {
		a.connectorsMu.Lock()
		index := a.connectorIndex(id)
		if index == -1 || !a.connectors[index].Enabled {
			a.connectorsMu.Unlock()
			return
		}
		item := a.connectors[index]
		a.connectors[index].Status = "starting"
		a.connectorsMu.Unlock()
		environment, err := readEnvironmentFile(filepath.Join(a.connectorEnvDir, id+".env"))
		if err == nil {
			environment = append(environment,
				"SLAXI_AXIS_URL="+a.connectorURL(),
				"SLAXI_AXIS_USERNAME="+a.username,
				"SLAXI_AXIS_PASSWORD="+a.password,
				"SLAXI_BOT_ID="+item.BotID,
				"SLAXI_PROJECT_ID="+item.ProjectID,
				"SLAXI_SESSION_DIR="+filepath.Join(a.root, "connectors", id),
			)
			command := exec.CommandContext(ctx, "slaxi")
			command.Env = overlayEnvironment(os.Environ(), environment)
			a.setConnectorStatus(id, "running", "")
			err = command.Run()
		}
		if ctx.Err() != nil {
			a.setConnectorStatus(id, "stopped", "")
			return
		}
		a.connectorsMu.Lock()
		index = a.connectorIndex(id)
		if index != -1 {
			a.connectors[index].Restarts++
		}
		a.connectorsMu.Unlock()
		message := "connector exited"
		if err != nil {
			message = err.Error()
		}
		a.setConnectorStatus(id, "failed", message)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay *= 2
			if delay > 15*time.Second {
				delay = 15 * time.Second
			}
		}
	}
}

func (a *app) setConnectorStatus(id, status, message string) {
	a.connectorsMu.Lock()
	if index := a.connectorIndex(id); index != -1 {
		a.connectors[index].Status = status
		a.connectors[index].Error = message
	}
	a.connectorsMu.Unlock()
}

func (a *app) connectorIndex(id string) int {
	for i, item := range a.connectors {
		if item.ID == id {
			return i
		}
	}
	return -1
}

func (a *app) connectorURL() string {
	if value := os.Getenv("AXIS_CONNECTOR_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	address := os.Getenv("AXIS_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	return "http://" + address
}

func (a *app) writeConnectors() error {
	if err := os.MkdirAll(filepath.Dir(a.connectorsPath), 0700); err != nil {
		return err
	}
	items := make([]connector, len(a.connectors))
	for i, item := range a.connectors {
		item.Status = ""
		item.Error = ""
		item.Restarts = 0
		items[i] = item
	}
	data, err := json.MarshalIndent(connectorsConfig{Connectors: items}, "", "  ")
	if err != nil {
		return err
	}
	temporary := a.connectorsPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, a.connectorsPath)
}
