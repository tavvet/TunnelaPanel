package panel

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const passwordIterations = 600000

type authFile struct {
	Username string `json:"username"`
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
}

type session struct {
	CSRF    string
	Expires time.Time
}

type attempt struct {
	Count int
	Until time.Time
}

type auth struct {
	mu       sync.Mutex
	file     authFile
	dir      string
	sessions map[string]session
	attempts map[string]attempt
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func passwordHash(password, salt string) (string, error) {
	hash, err := pbkdf2.Key(sha256.New, password, []byte(salt), passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(hash), nil
}

func atomicJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func newAuth(dir string) (*auth, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	a := &auth{dir: dir, sessions: make(map[string]session), attempts: make(map[string]attempt)}
	b, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err == nil {
		if err = json.Unmarshal(b, &a.file); err != nil {
			return nil, errors.New("invalid admin authentication state")
		}
		decoded, err := base64.RawStdEncoding.DecodeString(a.file.Hash)
		if err != nil || len(decoded) != 32 || len(a.file.Salt) < 32 || a.file.Username != "admin" {
			return nil, errors.New("invalid admin authentication state")
		}
		return a, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	passwordPath := filepath.Join(dir, "bootstrap-password")
	passwordBytes, err := os.ReadFile(passwordPath)
	if errors.Is(err, os.ErrNotExist) {
		passwordBytes = []byte(randomToken())
		if err := os.WriteFile(passwordPath, passwordBytes, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(passwordBytes) < 12 {
		return nil, errors.New("invalid bootstrap password file")
	}
	a.file = authFile{Username: "admin", Salt: randomToken()}
	a.file.Hash, err = passwordHash(string(passwordBytes), a.file.Salt)
	if err != nil {
		return nil, err
	}
	if err := atomicJSON(filepath.Join(dir, "auth.json"), a.file); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *auth) check(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkLocked(username, password)
}

func (a *auth) checkLocked(username, password string) bool {
	hash, err := passwordHash(password, a.file.Salt)
	return err == nil && subtle.ConstantTimeCompare([]byte(hash), []byte(a.file.Hash)) == 1 && username == a.file.Username
}

// Authentication and session creation must use the same credential generation.
func (a *auth) authenticate(username, password string, now time.Time) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.checkLocked(username, password) {
		return "", false
	}
	id, _ := a.createSessionLocked(now)
	return id, true
}

func (a *auth) allowAttempt(ip string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, key)
		}
	}
	v := a.attempts[ip]
	if v.Until.IsZero() {
		v.Until = now.Add(15 * time.Minute)
	}
	if v.Count >= 10 || len(a.attempts) >= 4096 {
		return false
	}
	v.Count++
	a.attempts[ip] = v
	return true
}

func (a *auth) clearAttempts(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

func (a *auth) createSession(now time.Time) (string, session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.createSessionLocked(now)
}

func (a *auth) createSessionLocked(now time.Time) (string, session) {
	for key, v := range a.sessions {
		if now.After(v.Expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) > 100 {
		a.sessions = make(map[string]session)
	}
	id := randomToken()
	s := session{CSRF: randomToken(), Expires: now.Add(8 * time.Hour)}
	a.sessions[id] = s
	return id, s
}

func (a *auth) lookup(id string, now time.Time) (session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if ok && now.After(s.Expires) {
		delete(a.sessions, id)
		return session{}, false
	}
	return s, ok
}

func (a *auth) removeSession(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
}

func (a *auth) changePassword(current, password string) error {
	if len(password) < 12 || len(password) > 128 {
		return errors.New("Пароль должен содержать от 12 до 128 символов.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.checkLocked("admin", current) {
		return errors.New("Текущий пароль неверен.")
	}
	f := authFile{Username: "admin", Salt: randomToken()}
	var err error
	f.Hash, err = passwordHash(password, f.Salt)
	if err != nil {
		return errors.New("Не удалось обновить пароль.")
	}
	if err := atomicJSON(filepath.Join(a.dir, "auth.json"), f); err != nil {
		return errors.New("Не удалось сохранить пароль.")
	}
	a.file = f
	a.sessions = make(map[string]session)
	if err := os.Remove(filepath.Join(a.dir, "bootstrap-password")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("Пароль изменён. Не удалось удалить файл первоначального пароля.")
	}
	return nil
}
