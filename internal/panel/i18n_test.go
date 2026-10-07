package panel

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func TestLocaleCatalogsHaveMatchingKeysAndFormatArguments(t *testing.T) {
	en, ru := catalogs["en"], catalogs["ru"]
	if len(en) == 0 || len(en) != len(ru) {
		t.Fatalf("catalog key counts differ: English=%d Russian=%d", len(en), len(ru))
	}
	verbs := regexp.MustCompile(`%[a-zA-Z]`)
	for key, english := range en {
		russian, ok := ru[key]
		if !ok || strings.TrimSpace(english) == "" || strings.TrimSpace(russian) == "" {
			t.Errorf("translation %q is missing or empty", key)
			continue
		}
		if !reflect.DeepEqual(verbs.FindAllString(english, -1), verbs.FindAllString(russian, -1)) {
			t.Errorf("translation %q has incompatible format arguments", key)
		}
	}
	if got := translate("en", "ui.common.settings"); got != "Settings" {
		t.Fatalf("English settings translation = %q", got)
	}
	if got := translate("ru", "ui.common.settings"); got != "Настройки" {
		t.Fatalf("Russian settings translation = %q", got)
	}
}

func TestReferencedTranslationKeysExistInBothLanguages(t *testing.T) {
	keyPattern := regexp.MustCompile(`"((?:ui|title|error|notice|format)\.[a-z0-9_.]+)"`)
	check := func(name string, source []byte) {
		t.Helper()
		for _, match := range keyPattern.FindAllSubmatch(source, -1) {
			key := string(match[1])
			for _, language := range []string{"en", "ru"} {
				if catalogs[language][key] == "" {
					t.Errorf("%s references missing %s translation %q", name, language, key)
				}
			}
		}
	}

	names, err := fs.Glob(assets, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := assets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		check(name, data)
	}
	names, err = filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		check(name, data)
	}
}

func TestUIErrorsAreLocalizedAndEndpointDiagnosticsStayOriginal(t *testing.T) {
	external := errors.New("error.csrf")
	for _, language := range []string{"en", "ru"} {
		localized := translate(language, "error.csrf")
		if got := errorText(language, uiError("error.csrf")); got != localized {
			t.Fatalf("typed error was not localized for %s", language)
		}
		if got := errorText(language, external); got != external.Error() {
			t.Fatal("external diagnostic was interpreted as a translation key")
		}
		joined := errors.Join(uiError("error.csrf"), external)
		if got := errorText(language, joined); got != localized+"\n"+external.Error() {
			t.Fatal("joined errors lost a message or changed an external diagnostic")
		}
	}
}

func TestLocalizedTemplateFormatsPreserveValues(t *testing.T) {
	templates, err := newTemplates()
	if err != nil {
		t.Fatal(err)
	}
	_, err = templates.New("format-probe").Parse(`{{bytes .Language .Bytes}}|{{datetime .Language .At}}|{{remaining .Language .Expires}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		language, expected string
	}{
		{"en", "1.0 KiB|2026-10-07 05:09 UTC|1 day"},
		{"ru", "1.0 КиБ|07.10.2026 05:09 UTC|1 дн."},
	} {
		var output bytes.Buffer
		err := templates.ExecuteTemplate(&output, "format-probe", struct {
			Language string
			Bytes    uint64
			At       time.Time
			Expires  time.Time
		}{
			Language: tc.language,
			Bytes:    1024,
			At:       time.Date(2026, 10, 7, 8, 9, 0, 0, time.FixedZone("UTC+3", 3*60*60)),
			Expires:  time.Now().Add(36 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if output.String() != tc.expected {
			t.Fatalf("%s formats = %q, want %q", tc.language, output.String(), tc.expected)
		}
	}
}

func TestTranslationArgumentsRemainEscapedInHTML(t *testing.T) {
	templates, err := newTemplates()
	if err != nil {
		t.Fatal(err)
	}
	username := `"><img src=x onerror=alert(1)>`
	for _, language := range []string{"en", "ru"} {
		var output bytes.Buffer
		err := templates.ExecuteTemplate(&output, "users", View{
			Language: language,
			Page:     "users",
			Users: []protocol.User{{
				Username: username,
				Enabled:  true,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "<img src=x") || !strings.Contains(output.String(), "&lt;img") {
			t.Fatalf("%s template did not escape a translated username argument", language)
		}
	}
}
