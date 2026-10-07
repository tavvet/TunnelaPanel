package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const defaultLanguage = "en"

type settingsFile struct {
	Language string `json:"language"`
}

// panelSettings belongs to the single administrator and applies to the whole UI,
// including login. It is independent of TrustTunnel's applied and draft state.
type panelSettings struct {
	mu    sync.RWMutex
	path  string
	value settingsFile
}

func validLanguage(language string) bool {
	return language == "en" || language == "ru"
}

func newPanelSettings(stateDir string) (*panelSettings, error) {
	s := &panelSettings{
		path:  filepath.Join(stateDir, "settings.json"),
		value: settingsFile{Language: defaultLanguage},
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		// Older installations have no preferences file. Use English without
		// rewriting their authentication state or creating a file until a save.
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read panel settings: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var saved settingsFile
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("invalid panel settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || !validLanguage(saved.Language) {
		return nil, errors.New("invalid panel settings: expected a single object with language en or ru")
	}
	s.value = saved
	return s, nil
}

func (s *panelSettings) language() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value.Language
}

func (s *panelSettings) setLanguage(language string) error {
	if !validLanguage(language) {
		return uiError("error.language_invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	value := settingsFile{Language: language}
	// Publish only after the atomic write succeeds, so a failed save cannot
	// change the UI temporarily and then revert on the next process start.
	if err := atomicJSON(s.path, value); err != nil {
		return uiError("error.settings_save")
	}
	s.value = value
	return nil
}

func (s *Server) preferences(w http.ResponseWriter, r *http.Request, _ session) {
	language := r.PostForm.Get("language")
	if !validLanguage(language) {
		s.audit("settings/preferences", s.remoteIP(r), false)
		http.Error(w, translate(requestLanguage(r), "error.language_invalid"), http.StatusBadRequest)
		return
	}
	if err := s.settings.setLanguage(language); err != nil {
		s.audit("settings/preferences", s.remoteIP(r), false)
		http.Error(w, errorText(requestLanguage(r), err), http.StatusInternalServerError)
		return
	}

	s.audit("settings/preferences", s.remoteIP(r), true)
	http.Redirect(w, r, "/settings?notice=preferences", http.StatusSeeOther)
}
