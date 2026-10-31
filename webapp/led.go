package main

// Status LED (-led, a sysfs brightness file). Solid = registered on the network,
// slow blink = modem present but not registered, fast blink for a minute after a
// new SMS, off = no modem or this program isn't running. The kernel has no timer
// trigger for this LED, so blinking is done here.

import (
	"errors"
	"log"
	"os"
	"sync/atomic"
	"time"
)

const (
	ledOff int32 = iota
	ledOn
	ledSlow
)

type statusLED struct {
	mode     atomic.Int32
	smsUntil atomic.Int64 // unix nanos until which to fast-blink
	seen     map[string]bool
	primed   bool // first SMS list seen; messages already there don't blink
}

func newStatusLED(path string) *statusLED {
	l := &statusLED{seen: map[string]bool{}}
	go l.run(path)
	return l
}

func (l *statusLED) update(s mmSnapshot, err error) {
	switch {
	case errors.Is(err, errNoModem):
		l.mode.Store(ledOff)
	case err != nil:
		// ModemManager restarting: keep the last pattern
	case s.registered():
		l.mode.Store(ledOn)
	default:
		l.mode.Store(ledSlow)
	}
}

func (l *statusLED) messages(list []sms) {
	for _, m := range list {
		if k := smsKey(m); !l.seen[k] {
			l.seen[k] = true
			if l.primed {
				l.smsUntil.Store(time.Now().Add(time.Minute).UnixNano())
			}
		}
	}
	l.primed = true
}

func (l *statusLED) run(path string) {
	last := -1
	for t := range time.Tick(100 * time.Millisecond) {
		ms := t.UnixMilli()
		val := 0
		switch {
		case t.UnixNano() < l.smsUntil.Load():
			val = int(ms / 100 % 2)
		case l.mode.Load() == ledOn:
			val = 1
		case l.mode.Load() == ledSlow:
			val = int(ms / 1000 % 2)
		}
		if val != last {
			if err := os.WriteFile(path, []byte{byte('0' + val)}, 0); err != nil {
				log.Printf("status LED off: %v", err)
				return
			}
			last = val
		}
	}
}
