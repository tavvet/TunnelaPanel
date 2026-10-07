package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPanelSettingsDefaultAndPersistencePreserveCredentials(t *testing.T) {
	dir := t.TempDir()
	privateFiles := map[string][]byte{
		"auth.json":          []byte("existing authentication data"),
		"bootstrap-password": []byte("existing bootstrap credential"),
	}
	for name, contents := range privateFiles {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}

	settings, err := newPanelSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.language(); got != "en" {
		t.Fatalf("missing settings file selected %q, want English", got)
	}

	for _, language := range []string{"ru", "en"} {
		if err := settings.setLanguage(language); err != nil {
			t.Fatal(err)
		}
		if got := settings.language(); got != language {
			t.Fatalf("published language = %q, want %q", got, language)
		}

		data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]string
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored) != 1 || stored["language"] != language {
			t.Fatal("settings file did not store the selected language independently")
		}

		reloaded, err := newPanelSettings(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := reloaded.language(); got != language {
			t.Fatalf("reloaded language = %q, want %q", got, language)
		}
	}

	for name, want := range privateFiles {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("language preference changed %s", name)
		}
	}
}

func TestPanelSettingsRejectInvalidPersistedData(t *testing.T) {
	for _, contents := range []string{
		`not JSON`,
		`{"language":"de"}`,
		`{"language":"EN"}`,
		`{"language":""}`,
		`{"language":null}`,
		`{"language":17}`,
		`{}`,
		`null`,
		`[]`,
		`{"language":"en"} trailing data`,
	} {
		t.Run(contents, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}

			if _, err := newPanelSettings(dir); err == nil {
				t.Fatal("invalid settings were silently accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != contents {
				t.Fatal("loading invalid settings replaced the original file")
			}
		})
	}
}

func TestPanelStartupRejectsInvalidSettingsWithoutChangingCredentials(t *testing.T) {
	dir := t.TempDir()
	config := Config{StateDir: dir}
	if _, err := New(config); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"auth.json", "bootstrap-password"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}

	for _, contents := range []string{`broken JSON`, `{"language":"de"}`} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(config); err == nil {
			t.Fatal("panel started with invalid persisted settings")
		}
		for name, want := range before {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("failed startup changed %s", name)
			}
		}
	}
}

func TestPanelSettingsRejectInvalidChangesWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	settings, err := newPanelSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.setLanguage("ru"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, language := range []string{"", "de", "EN", " ru ", "en-US"} {
		if err := settings.setLanguage(language); err == nil {
			t.Fatalf("accepted unsupported language %q", language)
		}
		if settings.language() != "ru" {
			t.Fatal("invalid language replaced the published preference")
		}
		after, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid language changed the settings file")
		}
	}
}

func TestPanelSettingsWriteFailureDoesNotPublishLanguage(t *testing.T) {
	dir := t.TempDir()
	settings, err := newPanelSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.setLanguage("en"); err != nil {
		t.Fatal(err)
	}

	// A directory at the destination makes atomic replacement fail even as root.
	path := filepath.Join(dir, "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}

	if err := settings.setLanguage("ru"); err == nil {
		t.Fatal("settings write unexpectedly succeeded")
	}
	if settings.language() != "en" {
		t.Fatal("failed settings write published a new language")
	}
}

func TestPanelSettingsConcurrentReadsAndWritesStayConsistent(t *testing.T) {
	dir := t.TempDir()
	settings, err := newPanelSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.setLanguage("en"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 24; iteration++ {
				if worker < 2 {
					language := []string{"en", "ru"}[(worker+iteration)%2]
					if err := settings.setLanguage(language); err != nil {
						failures <- err
						return
					}
				}
				if got := settings.language(); got != "en" && got != "ru" {
					failures <- fmt.Errorf("reader observed unsupported language %q", got)
					return
				}

				data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
				var stored struct {
					Language string `json:"language"`
				}
				if err != nil || json.Unmarshal(data, &stored) != nil ||
					(stored.Language != "en" && stored.Language != "ru") {
					failures <- fmt.Errorf("reader observed missing or partially written settings")
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	reloaded, err := newPanelSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.language() != settings.language() {
		t.Fatal("persisted language does not match the final published preference")
	}
}
