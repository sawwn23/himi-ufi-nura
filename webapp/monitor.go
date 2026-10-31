package main

// Background polling shared by the Telegram forwarder and the status LED. One
// GetManagedObjects call returns the modem, its state and its SMS list; on the
// stick that costs ~25 ms CPU, against ~60-85 ms for each mmcli invocation.

import (
	"encoding/json"
	"sort"
	"time"
)

type variant struct {
	Data json.RawMessage `json:"data"`
}

type mmSnapshot struct {
	path  string   // modem object path
	state int32    // MMModemState: 8 registered ... 11 connected, 2 locked, 7 searching
	sms   []string // SMS object paths
}

func (s mmSnapshot) registered() bool { return s.state >= 8 } // registered, disconnecting, connecting, connected

func snapshot() (mmSnapshot, error) {
	out, err := run(10*time.Second, "busctl", "--json=short", "call", mmBus, "/org/freedesktop/ModemManager1",
		"org.freedesktop.DBus.ObjectManager", "GetManagedObjects")
	if err != nil {
		return mmSnapshot{}, err
	}
	var v struct {
		Data []map[string]map[string]map[string]variant `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return mmSnapshot{}, err
	}
	if len(v.Data) == 0 {
		return mmSnapshot{}, errNoModem
	}
	paths := make([]string, 0, len(v.Data[0]))
	for p := range v.Data[0] {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		ifs := v.Data[0][p]
		m, ok := ifs[mmBus+".Modem"]
		if !ok {
			continue
		}
		s := mmSnapshot{path: p}
		json.Unmarshal(m["State"].Data, &s.state)
		json.Unmarshal(ifs[mmBus+".Modem.Messaging"]["Messages"].Data, &s.sms)
		return s, nil
	}
	return mmSnapshot{}, errNoModem
}

// monitorLoop polls every 5 s and hands received SMS (oldest first) to the
// forwarder and the modem state to the LED; either may be nil.
func monitorLoop(fwd *forwarder, led *statusLED) {
	for ; ; time.Sleep(5 * time.Second) {
		s, err := snapshot()
		if led != nil {
			led.update(s, err)
		}
		if err != nil {
			continue // no modem, or ModemManager restarting
		}
		var list []sms
		for _, p := range s.sms {
			if m, err := loadSMS(p); err == nil && m.State == "received" {
				list = append(list, m)
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Timestamp < list[j].Timestamp })
		if led != nil {
			led.messages(list)
		}
		if fwd != nil {
			fwd.forward(list)
		}
	}
}
