package panel

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	passwordIterations = 600000

	// Each client key gets loginAttempts tries per loginWindow. The table is
	// bounded; when full, the oldest key that still has tries left is evicted,
	// so exhausted keys stay blocked and rotating keys gains no extra tries.
	loginAttempts    = 10
	loginWindow      = 15 * time.Minute
	maxLoginAttempts = 4096

	// loginHashSlots bounds concurrent PBKDF2 derivations so a login flood
	// cannot take every CPU core from the VPN endpoint on the same host; with
	// the reserved slot below, at most loginHashSlots+1 run at once.
	// Every attempt, including an oversized password, is counted while a slot
	// is held and costs one derivation, which also caps how fast the attempt
	// table can fill with exhausted keys.
	loginHashSlots = 2
	loginHashWait  = 3 * time.Second

	// One extra slot is reserved for keys that signed in successfully before,
	// so a flood from many fresh addresses cannot starve the administrator.
	// Only a correct password adds a key, so attackers cannot claim it.
	maxTrustedKeys = 16
	trustedFor     = 30 * 24 * time.Hour

	// Refused logins are audited at most once per key and window, and refusals
	// without a key entry at most once per refusalReportEvery, so a flood
	// cannot push earlier records out of the rotated audit log.
	refusalReportEvery = time.Minute
)

type authFile struct {
	Username string `json:"username"`
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
}

type session struct {
	CSRF    string
	Expires time.Time
}

// refusal names a kind of refused login; the value is its audit action.
type refusal string

const (
	refusalRateLimited refusal = "login_rate_limited"
	refusalBusy        refusal = "login_busy"
)

type attempt struct {
	Count    int
	Until    time.Time
	Reported bool
}

// auth keeps credentials on disk, but sessions and rate limits only in memory.
// The mutex also serializes credential checks with password changes.
type auth struct {
	mu       sync.Mutex
	file     authFile
	dir      string
	sessions map[string]session
	attempts map[string]attempt

	hashSlots    chan struct{}
	reservedSlot chan struct{}
	trusted      map[string]time.Time
	// lastReport holds the last unkeyed refusal audit time per refusal kind.
	lastReport map[refusal]time.Time
}

// newAuthState creates the in-memory sessions, limits and hashing slots;
// newAuth adds the persisted credentials.
func newAuthState() *auth {
	return &auth{
		sessions:  make(map[string]session),
		attempts:  make(map[string]attempt),
		hashSlots: make(chan struct{}, loginHashSlots),
		// One slot, available only to trusted keys.
		reservedSlot: make(chan struct{}, 1),
		trusted:      make(map[string]time.Time),
		lastReport:   make(map[refusal]time.Time),
	}
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

// atomicJSON writes a private temporary file beside the destination, then replaces it.
// Syncing the file before rename and the directory afterward persists both steps.
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

	a := newAuthState()
	a.dir = dir

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

	// Reuse a bootstrap password left by an interrupted first start so the saved
	// password still matches the credentials that are about to be committed.
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

	a.file = authFile{
		Username: "admin",
		Salt:     randomToken(),
	}

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
	return err == nil && a.file.matches(username, hash)
}

// matches compares an already derived hash in constant time.
func (f authFile) matches(username, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(hash), []byte(f.Hash)) == 1 && username == f.Username
}

// acquireLoginSlot waits briefly for a hashing slot; false means the server is
// busy or the client went away. A trusted key may also use the reserved slot.
// The returned function releases whichever slot was taken; call it once.
func (a *auth) acquireLoginSlot(ctx context.Context, key string, wait time.Duration) (func(), bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	// A nil channel never becomes ready, which disables the reserved case.
	var reserved chan struct{}
	if a.isTrusted(key, time.Now()) {
		reserved = a.reservedSlot
	}

	select {
	case a.hashSlots <- struct{}{}:
		return func() { <-a.hashSlots }, true
	case reserved <- struct{}{}:
		return func() { <-reserved }, true
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

func (a *auth) isTrusted(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	since, ok := a.trusted[key]
	return ok && now.Sub(since) < trustedFor
}

// rejectSlowly spends the work of checking a wrong password, so an attempt
// that cannot match costs as much as any other and gains nothing.
func (a *auth) rejectSlowly() {
	a.mu.Lock()
	salt := a.file.Salt
	a.mu.Unlock()

	_, _ = passwordHash("", salt)
}

// loginSucceeded resets the key's limit and trusts it for the reserved slot.
func (a *auth) loginSucceeded(key string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.attempts, key)
	for k, since := range a.trusted {
		if now.Sub(since) >= trustedFor {
			delete(a.trusted, k)
		}
	}

	if _, ok := a.trusted[key]; !ok && len(a.trusted) >= maxTrustedKeys {
		var oldest string
		var at time.Time
		for k, since := range a.trusted {
			if at.IsZero() || since.Before(at) {
				oldest, at = k, since
			}
		}

		delete(a.trusted, oldest)
	}

	a.trusted[key] = now
}

// reportRefusal says whether a refused login should be audited: once per key
// and window while the key has an entry, otherwise once per kind and
// refusalReportEvery.
func (a *auth) reportRefusal(kind refusal, key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if v, ok := a.attempts[key]; ok && kind == refusalRateLimited {
		if v.Reported {
			return false
		}

		v.Reported = true
		a.attempts[key] = v
		return true
	}

	if now.Sub(a.lastReport[kind]) < refusalReportEvery {
		return false
	}

	a.lastReport[kind] = now
	return true
}

// authenticate derives the password hash without holding the lock, so slow
// login attempts do not stall session lookups for signed-in requests. The
// credentials are compared and the session created under the lock, and only if
// the hashed snapshot is still current: a concurrent password change cannot
// leave a new session using old credentials.
func (a *auth) authenticate(username, password string, now time.Time) (string, bool) {
	a.mu.Lock()
	file := a.file
	a.mu.Unlock()

	hash, err := passwordHash(password, file.Salt)
	if err != nil {
		return "", false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file != file || !file.matches(username, hash) {
		return "", false
	}

	id, _ := a.createSessionLocked(now)
	return id, true
}

// rateLimitKey groups IPv6 clients by /64, the smallest prefix normally
// assigned to one site, so rotating addresses inside it gives no extra tries.
func rateLimitKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() || addr.Is4In6() {
		return ip
	}

	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}

	return prefix.String()
}

// exhausted reports whether key has used all tries in its current window. It
// lets the handler refuse such a key before waiting for a hashing slot.
func (a *auth) exhausted(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	v, ok := a.attempts[key]
	return ok && !now.After(v.Until) && v.Count >= loginAttempts
}

func (a *auth) allowAttempt(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	for k, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, k)
		}
	}

	v, ok := a.attempts[key]
	if !ok {
		if len(a.attempts) >= maxLoginAttempts && !a.evictOldestAttemptLocked() {
			// Every key is exhausted; refuse rather than reset one of them.
			return false
		}

		v.Until = now.Add(loginWindow)
	}

	if v.Count >= loginAttempts {
		return false
	}

	v.Count++
	a.attempts[key] = v
	return true
}

// evictOldestAttemptLocked removes the earliest-expiring window that still has
// tries left. Exhausted keys are never evicted, so an attacker cannot reset
// its own limit by rotating through more keys than the table holds.
func (a *auth) evictOldestAttemptLocked() bool {
	var oldest string
	var until time.Time
	for k, v := range a.attempts {
		if v.Count < loginAttempts && (until.IsZero() || v.Until.Before(until)) {
			oldest, until = k, v.Until
		}
	}

	if until.IsZero() {
		return false
	}

	delete(a.attempts, oldest)
	return true
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

	// The form token is independent of the session ID sent in the HttpOnly cookie.
	id := randomToken()
	s := session{
		CSRF:    randomToken(),
		Expires: now.Add(8 * time.Hour),
	}

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
		return uiError("error.password_length")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.checkLocked("admin", current) {
		return uiError("error.password_current")
	}

	f := authFile{
		Username: "admin",
		Salt:     randomToken(),
	}

	var err error
	f.Hash, err = passwordHash(password, f.Salt)
	if err != nil {
		return uiError("error.password_update")
	}

	// Persist credentials before publishing them in memory and invalidating sessions.
	if err := atomicJSON(filepath.Join(a.dir, "auth.json"), f); err != nil {
		return uiError("error.password_save")
	}

	a.file = f
	a.sessions = make(map[string]session)
	if err := os.Remove(filepath.Join(a.dir, "bootstrap-password")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return uiError("error.bootstrap_remove")
	}

	return nil
}
