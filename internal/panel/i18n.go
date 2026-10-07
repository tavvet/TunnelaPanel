package panel

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

//go:embed locales/*.json
var localeFiles embed.FS

// Catalogs are immutable after startup and shared by every request. UI copy and
// server messages use the same translator, with separate files for navigation.
var catalogs = loadCatalogs()

func loadCatalogs() map[string]map[string]string {
	result := make(map[string]map[string]string)
	for _, language := range []string{"en", "ru"} {
		messages := make(map[string]string)
		for _, name := range []string{language + ".json", "messages." + language + ".json"} {
			data, err := localeFiles.ReadFile("locales/" + name)
			if err != nil {
				panic(err)
			}
			var entries map[string]string
			if err := json.Unmarshal(data, &entries); err != nil {
				panic(fmt.Errorf("invalid embedded translation file %s: %w", name, err))
			}
			for key, value := range entries {
				if _, exists := messages[key]; exists || value == "" {
					panic(fmt.Errorf("duplicate or empty translation %s in %s", key, name))
				}
				messages[key] = value
			}
		}
		result[language] = messages
	}
	return result
}

// translate returns plain text; html/template remains responsible for escaping
// translated strings and their parameters in both HTML and data attributes.
func translate(language, key string, args ...any) string {
	message := catalogs[language][key]
	if message == "" {
		message = catalogs[defaultLanguage][key]
	}
	if message == "" {
		return key
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

type languageContextKey struct{}

func requestLanguage(r *http.Request) string {
	if language, ok := r.Context().Value(languageContextKey{}).(string); ok && validLanguage(language) {
		return language
	}
	return defaultLanguage
}

// uiError keeps domain code independent of the request's language. Its default
// Error text is English; handlers localize it when presenting it to a user.
type uiError string

func (e uiError) Error() string {
	return translate(defaultLanguage, string(e))
}

func errorText(language string, err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var messages []string
		for _, part := range joined.Unwrap() {
			messages = append(messages, errorText(language, part))
		}
		return strings.Join(messages, "\n")
	}
	var message uiError
	if errors.As(err, &message) {
		return translate(language, string(message))
	}
	// TrustTunnel diagnostics and process logs retain their original wording.
	return err.Error()
}
