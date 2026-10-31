package main

// Forward received SMS to a Telegram chat. -telegram-file holds "token=" and
// "chat_id=" lines (write it with -telegram-setup). Forwarded messages are
// recorded by content hash in "forwarded" next to it, because ModemManager
// renumbers SMS on every restart; a failed send is retried on the next poll.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type telegram struct {
	token, chatID string
}

func (t telegram) call(method string, params url.Values, out any) error {
	c := http.Client{Timeout: 40 * time.Second}
	resp, err := c.PostForm("https://api.telegram.org/bot"+t.token+"/"+method, params)
	if err != nil {
		// The URL contains the token; never let it reach the log.
		return errors.New(strings.ReplaceAll(err.Error(), t.token, "<token>"))
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (t telegram) send(text string) error {
	return t.call("sendMessage", url.Values{"chat_id": {t.chatID}, "text": {text}}, nil)
}

func loadTelegram(path string) (telegram, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return telegram{}, err
	}
	var t telegram
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "token":
			t.token = v
		case "chat_id":
			t.chatID = v
		}
	}
	if t.token == "" || t.chatID == "" {
		return t, errors.New(path + ": needs token= and chat_id=")
	}
	return t, nil
}

// telegramSetup asks for the bot token, waits for the user to message the
// bot, takes that chat's id, writes the file and sends a test message.
func telegramSetup(path string) error {
	fmt.Fprint(os.Stderr, "Bot token from @BotFather: ")
	tok, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	t := telegram{token: strings.TrimSpace(tok)}
	var me struct{ Username string }
	if err := t.call("getMe", nil, &me); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Now open Telegram and send any message to @%s (waiting 2 minutes)...\n", me.Username)
	offset := 0
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		var ups []struct {
			UpdateID int `json:"update_id"`
			Message  *struct {
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				From struct {
					Username string `json:"username"`
				} `json:"from"`
			} `json:"message"`
		}
		err := t.call("getUpdates", url.Values{"offset": {strconv.Itoa(offset)}, "timeout": {"25"}}, &ups)
		if err != nil {
			return err
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Chat.Type != "private" {
				continue
			}
			t.chatID = strconv.FormatInt(u.Message.Chat.ID, 10)
			if err := os.WriteFile(path, []byte("token="+t.token+"\nchat_id="+t.chatID+"\n"), 0o600); err != nil {
				return err
			}
			// Acknowledge the update so it is not delivered again.
			t.call("getUpdates", url.Values{"offset": {strconv.Itoa(offset)}, "timeout": {"0"}}, nil)
			if err := t.send("✅ LTE dongle will forward incoming SMS here."); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Linked chat with @%s; test message sent. Restart the service to apply.\n", u.Message.From.Username)
			return nil
		}
	}
	return errors.New("no message received; run setup again")
}

func smsKey(m sms) string {
	h := sha256.Sum256([]byte(m.Number + "\x00" + m.Timestamp + "\x00" + m.Text))
	return hex.EncodeToString(h[:16])
}

func formatSMS(m sms) string {
	ts := m.Timestamp
	if t, err := time.Parse("2006-01-02T15:04:05-07:00", ts); err == nil {
		ts = t.Format("Jan 2 15:04 (-07:00)")
	} else if t, err := time.Parse("2006-01-02T15:04:05Z07", ts); err == nil {
		ts = t.Format("Jan 2 15:04 (-07:00)")
	}
	return "📩 " + m.Number + " · " + ts + "\n\n" + m.Text
}

type forwarder struct {
	t        telegram
	seenFile string
	seen     map[string]bool
}

func newForwarder(t telegram, dir string) *forwarder {
	f := &forwarder{t: t, seenFile: filepath.Join(dir, "forwarded"), seen: map[string]bool{}}
	if b, err := os.ReadFile(f.seenFile); err == nil {
		for _, k := range strings.Fields(string(b)) {
			f.seen[k] = true
		}
	}
	log.Printf("forwarding SMS to Telegram (%d already forwarded)", len(f.seen))
	return f
}

// forward sends each received SMS (oldest first) exactly once.
func (f *forwarder) forward(list []sms) {
	for _, m := range list {
		k := smsKey(m)
		if f.seen[k] {
			continue
		}
		if err := f.t.send(formatSMS(m)); err != nil {
			log.Printf("forward SMS %s: %v", m.ID, err)
			return // keep order; retry on the next poll
		}
		f.seen[k] = true
		if fh, err := os.OpenFile(f.seenFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fh.WriteString(k + "\n")
			fh.Close()
		}
		log.Printf("forwarded SMS %s from %s", m.ID, m.Number)
	}
}
