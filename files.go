package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	fileSearchMaxVisited = 4000
	fileSearchMaxResults = 40
	skipDirs             = "target\x00node_modules"
)

type fileHit struct {
	Path string `json:"path"`
}

func isSkippedDir(name string) bool {
	for _, skip := range strings.Split(skipDirs, "\x00") {
		if name == skip {
			return true
		}
	}
	return false
}

func (a *app) searchProjectFiles(w http.ResponseWriter, r *http.Request) {
	item, ok := a.project(r.PathValue("id"))
	if !ok || item.Path == "" {
		http.NotFound(w, r)
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	pref, rest := []string{}, []string{}
	visited := 0
	a.walkProjectFiles(item.Path, "", query, &pref, &rest, &visited)
	sort.Strings(pref)
	sort.Strings(rest)
	pref = append(pref, rest...)
	if len(pref) > fileSearchMaxResults {
		pref = pref[:fileSearchMaxResults]
	}
	hits := make([]fileHit, 0, len(pref))
	for _, path := range pref {
		hits = append(hits, fileHit{Path: path})
	}
	writeJSON(w, http.StatusOK, hits)
}

func (a *app) walkProjectFiles(dir, rel, query string, pref, rest *[]string, visited *int) {
	if *visited > fileSearchMaxVisited || len(*pref)+len(*rest) >= fileSearchMaxResults*4 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		*visited++
		if *visited > fileSearchMaxVisited {
			return
		}
		name := entry.Name()
		isDir := entry.IsDir()
		if isDir && strings.HasPrefix(name, ".") {
			continue
		}
		path := name
		if rel != "" {
			path = rel + "/" + name
		}
		if isDir {
			if isSkippedDir(name) {
				continue
			}
			a.walkProjectFiles(filepath.Join(dir, name), path, query, pref, rest, visited)
		}
		lower := strings.ToLower(path)
		base := strings.ToLower(name)
		matched := query == "" || base == query || strings.HasPrefix(base, query) ||
			strings.HasPrefix(lower, query) || strings.Contains(lower, query)
		if !matched {
			continue
		}
		display := path
		if isDir {
			display += "/"
		}
		if query == "" || strings.HasPrefix(base, query) || strings.HasPrefix(lower, query) {
			*pref = append(*pref, display)
		} else {
			*rest = append(*rest, display)
		}
	}
}
