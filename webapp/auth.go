package main

// Password login. The password is stored as a PBKDF2 hash in -passwd-file
// (set it with -set-password). Sessions are HMAC-signed cookies keyed by a
// random secret kept next to the password file, so they survive restarts;
// changing the password invalidates every session.

import (
	"bufio"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pbkdfIter  = 200_000
	cookieName = "lte_session"
	sessionTTL = 30 * 24 * time.Hour
)

var b64 = base64.RawURLEncoding

type auth struct {
	hashLine string // "pbkdf2-sha256$iter$salt$hash"
	key      []byte // cookie signing key
	loginMu  sync.Mutex
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, b64.EncodeToString(salt), b64.EncodeToString(dk)), nil
}

func checkPassword(line, pw string) bool {
	f := strings.Split(line, "$")
	if len(f) != 4 || f[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err1 := strconv.Atoi(f[1])
	salt, err2 := b64.DecodeString(f[2])
	want, err3 := b64.DecodeString(f[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// setPassword prompts on the terminal (echo off) and writes the hash file.
func setPassword(path string) error {
	in := bufio.NewReader(os.Stdin)
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		stty := func(arg string) {
			c := exec.Command("stty", arg)
			c.Stdin = os.Stdin
			c.Run()
		}
		stty("-echo")
		defer func() { stty("echo"); fmt.Fprintln(os.Stderr) }()
		s, err := in.ReadString('\n')
		return strings.TrimRight(s, "\r\n"), err
	}
	pw, err := read("New web UI password: ")
	if err != nil {
		return err
	}
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if again, err := read("Repeat: "); err != nil || again != pw {
		return errors.New("passwords do not match")
	}
	line, err := hashPassword(pw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(line+"\n"), 0o600)
}

func loadAuth(passwdFile string) (*auth, error) {
	b, err := os.ReadFile(passwdFile)
	if err != nil {
		return nil, fmt.Errorf("%w (create it with -set-password)", err)
	}
	a := &auth{hashLine: strings.TrimSpace(string(b))}
	keyFile := filepath.Join(filepath.Dir(passwdFile), "session.key")
	if k, err := os.ReadFile(keyFile); err == nil && len(k) >= 32 {
		a.key = k
	} else {
		a.key = make([]byte, 32)
		rand.Read(a.key)
		if err := os.WriteFile(keyFile, a.key, 0o600); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// sign binds the expiry to the current password hash.
func (a *auth) sign(exp string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(exp + "|" + a.hashLine))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *auth) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(exp))) {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && time.Now().Unix() < t
}

func setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	if err := readBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	// Serialize attempts and pause after a miss: at most ~1 guess per second.
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if !checkPassword(a.hashLine, req.Password) {
		time.Sleep(time.Second)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	exp := strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	setCookie(w, exp+"."+a.sign(exp), int(sessionTTL.Seconds()))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func logout(w http.ResponseWriter, r *http.Request) {
	setCookie(w, "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// sameOrigin rejects cross-site writes: browsers always send Origin on
// non-GET fetches, and it must match the host we are served from.
func sameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	o := r.Header.Get("Origin")
	return o == "" || strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://") == r.Host
}

// protect lets through the login page and API; everything else needs a session.
func (a *auth) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
			return
		}
		switch r.URL.Path {
		case "/login", "/api/login":
			next.ServeHTTP(w, r)
			return
		}
		if a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}
