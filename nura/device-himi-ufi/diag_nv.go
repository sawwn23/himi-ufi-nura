// diag_nv - read or write a Qualcomm modem NV item by ID over the SMD DIAG
// channel (a bound rpmsg_chrdev node, e.g. /dev/rpmsg0), using the classic
// QCDM DIAG commands 0x26 (NV_READ_F) and 0x27 (NV_WRITE_F).
//
// This is the raw NV path (not EFS). It exists to write NV item 2500
// (NV_FACTORY_DATA_4_I) = "HiMI_OK", which stops the HiMiLink baseband's
// ~900 s ML1 fatal (lte_ml1_common_timer.c:390) on this dongle.
//
// Framing is standard DIAG/HDLC: payload + CRC-16 (X.25 FCS) + byte-stuffing,
// terminated by 0x7e. Same scheme as ModemManager's libqcdm.
//
// Usage:
//   diag_nv [-dev /dev/rpmsg0] read  <item>
//   diag_nv [-dev /dev/rpmsg0] write <item> <hexbytes>   (<=128 bytes)
package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// CRC-16 X.25 / HDLC FCS: poly 0x8408 (reflected 0x1021), init 0xffff,
// final XOR 0xffff. This is the DM/QCDM CRC.
func fcs16(b []byte) uint16 {
	crc := uint16(0xffff)
	for _, c := range b {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

// hdlcEncode: append FCS, byte-stuff 0x7d/0x7e, add trailing 0x7e.
func hdlcEncode(payload []byte) []byte {
	crc := fcs16(payload)
	raw := append([]byte{}, payload...)
	raw = append(raw, byte(crc&0xff), byte(crc>>8))
	var out bytes.Buffer
	for _, c := range raw {
		switch c {
		case 0x7e:
			out.WriteByte(0x7d)
			out.WriteByte(0x5e)
		case 0x7d:
			out.WriteByte(0x7d)
			out.WriteByte(0x5d)
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte(0x7e)
	return out.Bytes()
}

// hdlcDecode: strip trailing 0x7e, un-stuff, drop the 2 FCS bytes. Returns the
// payload (CRC not re-verified; the modem's reply is trusted here).
func hdlcDecode(frame []byte) ([]byte, error) {
	if i := bytes.IndexByte(frame, 0x7e); i >= 0 {
		frame = frame[:i]
	}
	var out bytes.Buffer
	for i := 0; i < len(frame); i++ {
		if frame[i] == 0x7d && i+1 < len(frame) {
			i++
			out.WriteByte(frame[i] ^ 0x20)
		} else {
			out.WriteByte(frame[i])
		}
	}
	b := out.Bytes()
	if len(b) < 3 {
		return nil, errors.New("short frame")
	}
	return b[:len(b)-2], nil
}

// NV request/reply body: cmd(1) item(2 LE) data[128] status(2 LE) = 133 bytes.
func nvRequest(cmd byte, item int, data []byte) []byte {
	body := make([]byte, 133)
	body[0] = cmd
	body[1] = byte(item & 0xff)
	body[2] = byte((item >> 8) & 0xff)
	copy(body[3:131], data) // data is <=128 bytes; rest stays zero
	return body
}

func transact(dev string, req []byte) ([]byte, error) {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(req); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	// Read until a 0x7e terminator, in a goroutine so a silent modem can't
	// hang us: an rpmsg char device may not honor a read deadline.
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var acc bytes.Buffer
		buf := make([]byte, 512)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if bytes.IndexByte(buf[:n], 0x7e) >= 0 {
					ch <- res{acc.Bytes(), nil}
					return
				}
			}
			if err != nil {
				ch <- res{acc.Bytes(), err}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if len(r.b) == 0 {
			return nil, errors.New("no reply")
		}
		return hdlcDecode(r.b)
	case <-time.After(3 * time.Second):
		f.Close() // unblock the goroutine's Read
		return nil, errors.New("no reply (timeout)")
	}
}

func parseItem(s string) (int, error) {
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		v, err := strconv.ParseInt(s[2:], 16, 32)
		return int(v), err
	}
	v, err := strconv.Atoi(s)
	return v, err
}

func main() {
	dev := flag.String("dev", "/dev/rpmsg0", "bound DIAG rpmsg device")
	flag.Parse()
	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: diag_nv [-dev DEV] read <item> | write <item> <hex>")
		os.Exit(2)
	}
	item, err := parseItem(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad item:", err)
		os.Exit(2)
	}

	switch args[0] {
	case "read":
		reply, err := transact(*dev, hdlcEncode(nvRequest(0x26, item, nil)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "read failed:", err)
			os.Exit(1)
		}
		printReply(reply)
	case "write":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "write needs <hex>")
			os.Exit(2)
		}
		data, err := hex.DecodeString(args[2])
		if err != nil || len(data) > 128 {
			fmt.Fprintln(os.Stderr, "bad hex payload (<=128 bytes)")
			os.Exit(2)
		}
		reply, err := transact(*dev, hdlcEncode(nvRequest(0x27, item, data)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "write failed:", err)
			os.Exit(1)
		}
		printReply(reply)
		// Verify by reading back.
		rb, err := transact(*dev, hdlcEncode(nvRequest(0x26, item, nil)))
		if err == nil && len(rb) >= 131 && bytes.Equal(rb[3:3+len(data)], data) {
			fmt.Println("VERIFY: MATCH")
		} else {
			fmt.Println("VERIFY: MISMATCH")
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", args[0])
		os.Exit(2)
	}
}

func printReply(reply []byte) {
	if len(reply) < 3 {
		fmt.Printf("short reply (%d bytes): %x\n", len(reply), reply)
		return
	}
	fmt.Printf("cmd=0x%02x item=%d\n", reply[0], int(reply[1])|int(reply[2])<<8)
	if len(reply) >= 133 {
		data := reply[3:131]
		status := uint16(reply[131]) | uint16(reply[132])<<8
		fmt.Printf("status=0x%04x\n", status)
		// hexdump 128-byte payload, 16 bytes/line: "OOOO  xx .. xx  |ascii|"
		for off := 0; off < len(data); off += 16 {
			end := off + 16
			if end > len(data) {
				end = len(data)
			}
			fmt.Printf("%04x  ", off)
			for i := off; i < end; i++ {
				fmt.Printf("%02x ", data[i])
			}
			fmt.Printf(" |")
			for i := off; i < end; i++ {
				c := data[i]
				if c >= 0x20 && c < 0x7f {
					fmt.Printf("%c", c)
				} else {
					fmt.Printf(".")
				}
			}
			fmt.Printf("|\n")
		}
	} else {
		fmt.Printf("payload: %x\n", reply)
	}
}
