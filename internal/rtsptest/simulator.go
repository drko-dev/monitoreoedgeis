package rtsptest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Simulator is a minimal, deterministic RTSP server for exercising a real
// RTSP client end to end (Hito W3). It speaks just enough of the protocol to
// drive DESCRIBE / SETUP / PLAY / TEARDOWN over RTP/AVP/TCP interleaved
// transport, with deterministic SDP and RTP payloads.
//
// It is not a general-purpose RTSP server and does not reuse or duplicate
// the production client/parser in internal/rtsp — it is a hand-rolled,
// independent implementation of the wire protocol, exactly like a real
// camera would be.
type Simulator struct {
	listener net.Listener
	addr     string

	username string
	password string
	realm    string
	nonce    string
	sdp      string

	autoPacketCount    int
	autoPacketInterval time.Duration

	mu     sync.Mutex
	conns  []*simConn
	closed bool
}

type simConn struct {
	net.Conn
	sessionID string
	playing   atomic.Bool
}

// Options configures a Simulator. All fields are optional.
type Options struct {
	// Username/Password enable RTSP Digest auth on DESCRIBE when both are
	// set. When either is empty, DESCRIBE succeeds unauthenticated.
	Username string
	Password string

	// SDP overrides the deterministic default SDP body returned by
	// DESCRIBE. Defaults to a single H.264 video track.
	SDP string

	// AutoPacketCount, when > 0, makes the simulator automatically push
	// this many deterministic RTP packets (channel 0) after PLAY, spaced
	// AutoPacketInterval apart (default 20ms). Use SendPacket/CutStream
	// for manual control instead when AutoPacketCount is 0.
	AutoPacketCount    int
	AutoPacketInterval time.Duration
}

const defaultSDP = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=rtsptest\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:track1\r\n"

// NewSimulator starts a Simulator listening on a dynamically assigned
// loopback TCP port.
func NewSimulator(opts Options) (*Simulator, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("rtsptest: listen: %w", err)
	}
	sdp := opts.SDP
	if sdp == "" {
		sdp = defaultSDP
	}
	interval := opts.AutoPacketInterval
	if interval <= 0 {
		interval = 20 * time.Millisecond
	}
	s := &Simulator{
		listener:           l,
		addr:               l.Addr().String(),
		username:           opts.Username,
		password:           opts.Password,
		realm:              "rtsptest-realm",
		nonce:              "rtsptest-nonce",
		sdp:                sdp,
		autoPacketCount:    opts.AutoPacketCount,
		autoPacketInterval: interval,
	}
	go s.serve()
	return s, nil
}

// Addr returns the "host:port" the simulator is listening on.
func (s *Simulator) Addr() string { return s.addr }

// Close stops accepting connections and closes every connection accepted so
// far (equivalent to an abrupt camera disconnect for all sessions).
func (s *Simulator) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := append([]*simConn(nil), s.conns...)
	s.mu.Unlock()

	_ = s.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

// CutStream closes the most recently accepted connection without responding
// to TEARDOWN, simulating the camera dropping the stream mid-session.
func (s *Simulator) CutStream() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == 0 {
		return
	}
	_ = s.conns[len(s.conns)-1].Close()
}

// SendPacket pushes one RTP/AVP/TCP interleaved packet (channel 0) with the
// given payload to the most recently accepted, currently playing
// connection. It is a no-op if there is no playing connection.
func (s *Simulator) SendPacket(payload []byte) error {
	s.mu.Lock()
	var target *simConn
	for i := len(s.conns) - 1; i >= 0; i-- {
		if s.conns[i].playing.Load() {
			target = s.conns[i]
			break
		}
	}
	s.mu.Unlock()
	if target == nil {
		return nil
	}
	return writeInterleavedFrame(target, 0, payload)
}

func writeInterleavedFrame(conn net.Conn, channel byte, payload []byte) error {
	hdr := []byte{'$', channel, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(payload)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func (s *Simulator) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		sc := &simConn{Conn: conn}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns = append(s.conns, sc)
		s.mu.Unlock()
		go s.handleConn(sc)
	}
}

func (s *Simulator) handleConn(conn *simConn) {
	reader := bufio.NewReader(conn)

	for {
		reqLine, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		reqLine = strings.TrimSpace(reqLine)
		if reqLine == "" {
			continue
		}
		parts := strings.Fields(reqLine)
		if len(parts) < 2 {
			return
		}
		method, uri := parts[0], parts[1]

		headers := make(map[string]string)
		for {
			hLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			hLine = strings.TrimRight(hLine, "\r\n")
			if hLine == "" {
				break
			}
			if idx := strings.Index(hLine, ":"); idx != -1 {
				k := strings.ToLower(strings.TrimSpace(hLine[:idx]))
				v := strings.TrimSpace(hLine[idx+1:])
				headers[k] = v
			}
		}

		cseq := headers["cseq"]

		switch method {
		case "DESCRIBE":
			if !s.authOK(headers["authorization"], "DESCRIBE", uri) {
				resp := fmt.Sprintf("RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Digest realm=\"%s\", nonce=\"%s\"\r\n\r\n",
					cseq, s.realm, s.nonce)
				_, _ = conn.Write([]byte(resp))
				continue
			}
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s",
				cseq, len(s.sdp), s.sdp)
			_, _ = conn.Write([]byte(resp))

		case "SETUP":
			conn.sessionID = "rtsptest-session-1"
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\nSession: %s;timeout=60\r\n\r\n",
				cseq, conn.sessionID)
			_, _ = conn.Write([]byte(resp))

		case "PLAY":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\nRange: npt=0.000-\r\n\r\n",
				cseq, conn.sessionID)
			_, _ = conn.Write([]byte(resp))
			conn.playing.Store(true)

			if s.autoPacketCount > 0 {
				go s.sendAutoPackets(conn)
			}

		case "TEARDOWN":
			conn.playing.Store(false)
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\n\r\n", cseq, conn.sessionID)
			_, _ = conn.Write([]byte(resp))
			return
		}
	}
}

func (s *Simulator) sendAutoPackets(conn *simConn) {
	for i := 0; i < s.autoPacketCount && conn.playing.Load(); i++ {
		payload := []byte(fmt.Sprintf("rtp-packet-%d", i))
		if err := writeInterleavedFrame(conn, 0, payload); err != nil {
			return
		}
		time.Sleep(s.autoPacketInterval)
	}
}

// authOK validates RTSP Digest auth when the simulator was configured with
// credentials. It always returns true when no credentials were configured.
func (s *Simulator) authOK(authHeader, method, uri string) bool {
	if s.username == "" && s.password == "" {
		return true
	}
	if authHeader == "" {
		return false
	}
	ha1 := md5Hex(s.username + ":" + s.realm + ":" + s.password)
	ha2 := md5Hex(method + ":" + uri)
	expected := md5Hex(ha1 + ":" + s.nonce + ":" + ha2)
	return strings.Contains(authHeader, expected)
}
