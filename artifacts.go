package main

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *app) readArtifacts(session string) ([]artifact, error) {
	path, err := a.sessionPath(session)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(path, "artifacts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]artifact, 0, len(entries))
	for _, entry := range entries {
		id, name, ok := strings.Cut(entry.Name(), "-")
		if !ok || len(id) != 24 || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		mediaType := mime.TypeByExtension(filepath.Ext(name))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		items = append(items, artifact{ID: id, Name: name, MediaType: mediaType, Size: info.Size()})
	}
	return items, nil
}

func (a *app) getArtifact(w http.ResponseWriter, r *http.Request) {
	path, err := a.sessionPath(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("artifact")
	if len(id) != 24 || strings.ContainsAny(id, `/\\`) {
		http.NotFound(w, r)
		return
	}
	matches, err := filepath.Glob(filepath.Join(path, "artifacts", id+"-*"))
	if err != nil || len(matches) != 1 {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(matches[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(filepath.Base(matches[0]), id+"-")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	if mediaType := mime.TypeByExtension(filepath.Ext(name)); mediaType != "" {
		w.Header().Set("Content-Type", mediaType)
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
