package notifications

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn is a minimal RFC 6455 text-frame connection. It exists so this module
// does not take a new dependency (go.mod is Track A).
type wsConn struct {
	c   net.Conn
	r   *bufio.Reader
	mu  sync.Mutex
	srv bool
}

func (c *wsConn) close() error { return c.c.Close() }

func (c *wsConn) setDeadline(t time.Time) error { return c.c.SetDeadline(t) }

func (c *wsConn) writeText(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.c, payload, !c.srv)
}

func (c *wsConn) readText() ([]byte, error) {
	for {
		opcode, payload, err := readFrame(c.r)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1:
			return payload, nil
		case 0x8:
			return nil, io.EOF
		case 0x9:
			c.mu.Lock()
			err = writeOpcode(c.c, 0xA, payload, !c.srv)
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
		case 0xA:
			continue
		default:
			continue
		}
	}
}

func acceptWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		http.Error(w, "upgrade required", http.StatusBadRequest)
		return nil, errors.New("notifications: websocket upgrade required")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return nil, errors.New("notifications: websocket key missing")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("notifications: websocket hijack unsupported")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	accept := wsAccept(key)
	if _, err := fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &wsConn{c: conn, r: brw.Reader, srv: true}, nil
}

func dialWS(rawURL string) (*wsConn, error) {
	host := rawURL
	path := "/"
	host = strings.TrimPrefix(host, "ws://")
	host = strings.TrimPrefix(host, "http://")
	if i := strings.Index(host, "/"); i >= 0 {
		path = host[i:]
		host = host[:i]
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host, key)
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		_ = conn.Close()
		return nil, fmt.Errorf("notifications: websocket status %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &wsConn{c: conn, r: br, srv: false}, nil
}

func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerHasToken(v, token string) bool {
	for _, part := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func writeFrame(w io.Writer, payload []byte, mask bool) error {
	return writeOpcode(w, 0x1, payload, mask)
}

func writeOpcode(w io.Writer, opcode byte, payload []byte, mask bool) error {
	hdr := []byte{0x80 | opcode, 0}
	n := len(payload)
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 65535:
		hdr[1] = 126
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		hdr = append(hdr, ext[:]...)
	default:
		return errors.New("notifications: websocket frame too large")
	}
	var maskKey [4]byte
	if mask {
		hdr[1] |= 0x80
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if mask {
		if _, err := w.Write(maskKey[:]); err != nil {
			return err
		}
		buf := make([]byte, n)
		for i := range payload {
			buf[i] = payload[i] ^ maskKey[i%4]
		}
		_, err := w.Write(buf)
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	opcode := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	ln := int(h[1] & 0x7f)
	switch ln {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		ln = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		return 0, nil, errors.New("notifications: websocket frame too large")
	}
	if ln > 1<<20 {
		return 0, nil, errors.New("notifications: websocket frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, ln)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}
