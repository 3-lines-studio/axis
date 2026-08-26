package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type userCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func axCommandsDirectory() string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return ""
		}
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "ax", "commands")
}

// parseCommandFrontmatter mirrors AX's skills::parse_frontmatter.
func parseCommandFrontmatter(text string) (string, string) {
	rest, ok := strings.CutPrefix(text, "---")
	if !ok {
		return firstNonEmptyLine(text), text
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return firstNonEmptyLine(text), text
	}
	frontmatter := rest[:end]
	body := strings.TrimLeft(rest[end+4:], "\n ")
	description := ""
	for _, line := range strings.Split(frontmatter, "\n") {
		if key, value, found := strings.Cut(line, ":"); found && strings.TrimSpace(key) == "description" {
			description = strings.TrimSpace(value)
			break
		}
	}
	if description == "" {
		description = firstNonEmptyLine(body)
	}
	return description, body
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (a *app) listCommands(w http.ResponseWriter, _ *http.Request) {
	dir := axCommandsDirectory()
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, []userCommand{})
		return
	}
	items := make([]userCommand, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		description, _ := parseCommandFrontmatter(string(data))
		items = append(items, userCommand{
			Name:        strings.TrimSuffix(name, ".md"),
			Description: description,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, items)
}
