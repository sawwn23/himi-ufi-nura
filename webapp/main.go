// LTE dongle manager: a small web UI over ModemManager (mmcli + D-Bus).
//
//	go run . -addr 127.0.0.1:8080
//	lte-dongle -passwd-file /var/lib/lte-dongle/passwd -set-password
//	lte-dongle -telegram-file /var/lib/lte-dongle/telegram -telegram-setup
//	lte-dongle -addr :8080 -passwd-file /var/lib/lte-dongle/passwd -telegram-file /var/lib/lte-dongle/telegram
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

const mmBus = "org.freedesktop.ModemManager1"

var errNoModem = errors.New("no modem detected")

// run executes a command and returns stdout, folding stderr into the error.
func run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(strings.TrimPrefix(msg, "error: "))
	}
	return out, nil
}

func mmcli(args ...string) (string, error) {
	out, err := run(30*time.Second, "mmcli", args...)
	return strings.TrimSpace(string(out)), err
}

// mmcliJSON runs mmcli -J and decodes the output into a generic map.
func mmcliJSON(args ...string) (map[string]any, error) {
	out, err := run(15*time.Second, "mmcli", append([]string{"-J"}, args...)...)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// busctl calls a ModemManager D-Bus method and returns the decoded "data" array.
// Arguments go through argv, so SMS text needs no quoting or escaping.
func busctl(timeout time.Duration, path, iface, method string, args ...string) ([]any, error) {
	a := []string{"--json=short", "--timeout=" + timeout.String(), "call", mmBus, path, iface, method}
	out, err := run(timeout+5*time.Second, "busctl", append(a, args...)...)
	if err != nil {
		return nil, err
	}
	var v struct {
		Data []any `json:"data"`
	}
	if len(out) > 0 {
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, err
		}
	}
	return v.Data, nil
}

// modemPath returns the D-Bus path of the first modem. The index changes on
// every re-plug, so it is looked up on each request rather than cached.
func modemPath() (string, error) {
	s, err := snapshot()
	return s.path, err
}

func lastSeg(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// dig walks nested maps: dig(v, "modem", "generic", "sim").
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	if s == "--" {
		return ""
	}
	return s
}

// ---- SMS cache: received messages never change, so fetch each only once ----

type sms struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	Text      string `json:"text"`
	State     string `json:"state"`
	PDUType   string `json:"pduType"`
	Timestamp string `json:"timestamp"`
	Storage   string `json:"storage"`
}

var (
	smsMu    sync.Mutex
	smsCache = map[string]sms{}
)

func loadSMS(path string) (sms, error) {
	smsMu.Lock()
	m, ok := smsCache[path]
	smsMu.Unlock()
	if ok {
		return m, nil
	}
	v, err := mmcliJSON("-s", lastSeg(path))
	if err != nil {
		return sms{}, err
	}
	s := dig(v, "sms")
	m = sms{
		ID:        lastSeg(path),
		Number:    str(dig(s, "content", "number")),
		Text:      str(dig(s, "content", "text")),
		State:     str(dig(s, "properties", "state")),
		PDUType:   str(dig(s, "properties", "pdu-type")),
		Timestamp: str(dig(s, "properties", "timestamp")),
		Storage:   str(dig(s, "properties", "storage")),
	}
	// Outgoing messages change state (sending -> sent); only cache final ones.
	if m.State == "received" || m.State == "sent" {
		smsMu.Lock()
		smsCache[path] = m
		smsMu.Unlock()
	}
	return m, nil
}

// SIM properties (IMSI, ICCID, operator) don't change for a given SIM object.
var simCache sync.Map

func loadSIM(path string) (any, error) {
	if v, ok := simCache.Load(path); ok {
		return v, nil
	}
	v, err := mmcliJSON("-i", lastSeg(path))
	if err != nil {
		return nil, err
	}
	p := dig(v, "sim", "properties")
	simCache.Store(path, p)
	return p, nil
}

func listSMS() ([]sms, error) {
	s, err := snapshot()
	if err != nil {
		return nil, err
	}
	out := make([]sms, 0, len(s.sms))
	for _, p := range s.sms {
		m, err := loadSMS(p)
		if err != nil {
			continue // message vanished between list and fetch
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out, nil
}

// ---- HTTP plumbing ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, errNoModem) {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v)
}

// withModem resolves the modem and hands its index to the handler.
func withModem(h func(w http.ResponseWriter, r *http.Request, path, idx string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := modemPath()
		if err != nil {
			fail(w, err)
			return
		}
		h(w, r, p, lastSeg(p))
	}
}

// simple wraps an mmcli action that only reports success or failure.
func simple(args func(idx string) []string) http.HandlerFunc {
	return withModem(func(w http.ResponseWriter, r *http.Request, _, idx string) {
		out, err := mmcli(args(idx)...)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"result": out})
	})
}

var (
	reUSSD   = regexp.MustCompile(`^[0-9*#]{1,32}$`)
	rePhone  = regexp.MustCompile(`^\+?[0-9]{2,20}$`)
	reAPN    = regexp.MustCompile(`^[A-Za-z0-9.\-_]{0,63}$`)
	rePIN    = regexp.MustCompile(`^[0-9]{4,8}$`)
	reSMSID  = regexp.MustCompile(`^[0-9]+$`)
	ussdIfce = mmBus + ".Modem.Modem3gpp.Ussd"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; anything but loopback requires -passwd-file")
	passwdFile := flag.String("passwd-file", "", "password hash file; enables login")
	setPw := flag.Bool("set-password", false, "prompt for a new password, write -passwd-file and exit")
	tgFile := flag.String("telegram-file", "", "Telegram bot config; received SMS are forwarded if it exists")
	tgSetup := flag.Bool("telegram-setup", false, "link a Telegram bot, write -telegram-file and exit")
	led := flag.String("led", "", "status LED brightness file, e.g. /sys/class/leds/blue:wan/brightness")
	flag.Parse()

	if *tgSetup {
		if *tgFile == "" {
			log.Fatal("-telegram-setup needs -telegram-file")
		}
		if err := telegramSetup(*tgFile); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	if *setPw {
		if *passwdFile == "" {
			log.Fatal("-set-password needs -passwd-file")
		}
		if err := setPassword(*passwdFile); err != nil {
			log.Fatal(err)
		}
		log.Printf("password written to %s; restart the service to apply", *passwdFile)
		os.Exit(0)
	}

	var a *auth
	if *passwdFile != "" {
		var err error
		if a, err = loadAuth(*passwdFile); err != nil {
			log.Fatal(err)
		}
	} else if host, _, _ := net.SplitHostPort(*addr); !net.ParseIP(host).IsLoopback() && host != "localhost" {
		log.Fatal("refusing to listen on ", *addr, " without -passwd-file: this UI can send SMS")
	}

	mux := http.NewServeMux()

	// Everything the dashboard needs in one round-trip.
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		p, err := modemPath()
		if errors.Is(err, errNoModem) {
			writeJSON(w, 200, map[string]any{"present": false, "auth": a != nil})
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		idx := lastSeg(p)
		modem, err := mmcliJSON("-m", idx)
		if err != nil {
			fail(w, err)
			return
		}
		res := map[string]any{"present": true, "auth": a != nil, "index": idx, "modem": dig(modem, "modem")}
		if sig, err := mmcliJSON("-m", idx, "--signal-get"); err == nil {
			res["signal"] = dig(sig, "modem", "signal")
		}
		if sp := str(dig(modem, "modem", "generic", "sim")); sp != "" {
			if sim, err := loadSIM(sp); err == nil {
				res["sim"] = sim
			}
		}
		var bearers []any
		list, _ := dig(modem, "modem", "generic", "bearers").([]any)
		for _, b := range list {
			if bv, err := mmcliJSON("-b", lastSeg(b.(string))); err == nil {
				bearers = append(bearers, dig(bv, "bearer"))
			}
		}
		res["bearers"] = bearers
		writeJSON(w, 200, res)
	})

	mux.HandleFunc("GET /api/sms", func(w http.ResponseWriter, r *http.Request) {
		list, err := listSMS()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, list)
	})

	mux.HandleFunc("POST /api/sms", withModem(func(w http.ResponseWriter, r *http.Request, mp, _ string) {
		var req struct{ Number, Text string }
		if err := readBody(r, &req); err != nil {
			fail(w, err)
			return
		}
		req.Number = strings.ReplaceAll(req.Number, " ", "")
		if !rePhone.MatchString(req.Number) || req.Text == "" {
			fail(w, errors.New("need a valid number and non-empty text"))
			return
		}
		data, err := busctl(20*time.Second, mp, mmBus+".Modem.Messaging", "Create",
			"a{sv}", "2", "number", "s", req.Number, "text", "s", req.Text)
		if err != nil {
			fail(w, err)
			return
		}
		smsPath := data[0].(string)
		if _, err := mmcli("-s", lastSeg(smsPath), "--send"); err != nil {
			fail(w, errors.New("send failed: "+err.Error()))
			return
		}
		writeJSON(w, 200, map[string]string{"id": lastSeg(smsPath)})
	}))

	mux.HandleFunc("DELETE /api/sms/{id}", withModem(func(w http.ResponseWriter, r *http.Request, _, idx string) {
		id := r.PathValue("id")
		if !reSMSID.MatchString(id) {
			fail(w, errors.New("bad id"))
			return
		}
		if _, err := mmcli("-m", idx, "--messaging-delete-sms="+id); err != nil {
			fail(w, err)
			return
		}
		smsMu.Lock()
		delete(smsCache, "/org/freedesktop/ModemManager1/SMS/"+id)
		smsMu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/modem/enable", simple(func(i string) []string { return []string{"-m", i, "--enable"} }))
	mux.HandleFunc("POST /api/modem/disable", simple(func(i string) []string { return []string{"-m", i, "--disable"} }))
	mux.HandleFunc("POST /api/modem/reset", simple(func(i string) []string { return []string{"-m", i, "--reset"} }))
	mux.HandleFunc("POST /api/modem/signal-setup", simple(func(i string) []string { return []string{"-m", i, "--signal-setup=5"} }))
	mux.HandleFunc("POST /api/modem/disconnect", simple(func(i string) []string { return []string{"-m", i, "--simple-disconnect"} }))

	mux.HandleFunc("POST /api/modem/connect", withModem(func(w http.ResponseWriter, r *http.Request, _, idx string) {
		var req struct{ APN string }
		if err := readBody(r, &req); err != nil || !reAPN.MatchString(req.APN) {
			fail(w, errors.New("bad APN"))
			return
		}
		out, err := mmcli("-m", idx, "--simple-connect=apn="+req.APN+",ip-type=ipv4v6,allow-roaming=yes")
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"result": out})
	}))

	mux.HandleFunc("POST /api/sim/pin", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ PIN string }
		if err := readBody(r, &req); err != nil || !rePIN.MatchString(req.PIN) {
			fail(w, errors.New("PIN must be 4-8 digits"))
			return
		}
		mp, err := modemPath()
		if err != nil {
			fail(w, err)
			return
		}
		modem, err := mmcliJSON("-m", lastSeg(mp))
		if err != nil {
			fail(w, err)
			return
		}
		sp := str(dig(modem, "modem", "generic", "sim"))
		if sp == "" {
			fail(w, errors.New("no SIM"))
			return
		}
		out, err := mmcli("-i", lastSeg(sp), "--pin="+req.PIN)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"result": out})
	})

	// USSD: start a session, or reply when the network asks for input.
	mux.HandleFunc("POST /api/ussd", withModem(func(w http.ResponseWriter, r *http.Request, mp, _ string) {
		var req struct {
			Code  string
			Reply bool
		}
		if err := readBody(r, &req); err != nil || !reUSSD.MatchString(req.Code) {
			fail(w, errors.New("USSD code may only contain 0-9 * #"))
			return
		}
		method := "Initiate"
		if req.Reply {
			method = "Respond"
		}
		data, err := busctl(60*time.Second, mp, ussdIfce, method, "s", req.Code)
		if err != nil {
			fail(w, err)
			return
		}
		reply, _ := data[0].(string)
		writeJSON(w, 200, map[string]string{"reply": reply})
	}))

	mux.HandleFunc("POST /api/ussd/cancel", withModem(func(w http.ResponseWriter, r *http.Request, mp, _ string) {
		if _, err := busctl(10*time.Second, mp, ussdIfce, "Cancel"); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))

	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServerFS(static))

	var fwd *forwarder
	if *tgFile != "" {
		if t, err := loadTelegram(*tgFile); err == nil {
			fwd = newForwarder(t, filepath.Dir(*tgFile))
		} else {
			log.Printf("SMS forwarding off: %v", err)
		}
	}
	var sl *statusLED
	if *led != "" {
		sl = newStatusLED(*led)
	}
	if fwd != nil || sl != nil {
		go monitorLoop(fwd, sl)
	}

	var h http.Handler = mux
	if a != nil {
		mux.HandleFunc("POST /api/login", a.login)
		mux.HandleFunc("POST /api/logout", logout)
		mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, static, "login.html")
		})
		h = a.protect(mux)
	} else {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !sameOrigin(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
				return
			}
			mux.ServeHTTP(w, r)
		})
	}

	log.Printf("LTE dongle manager on http://%s (login %v)", *addr, a != nil)
	log.Fatal(http.ListenAndServe(*addr, h))
}
