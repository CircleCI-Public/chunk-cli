package fakes

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHServer is a TLS+SSH server for testing SSH-based sandbox interactions.
// It accepts a single authorized public key and records exec requests.
type SSHServer struct {
	listener net.Listener
	mu       sync.Mutex
	stdout   string
	exitCode int
	commands []string
}

// GenerateSSHKeypair generates an ed25519 keypair, writes the private and public
// key files to a temp directory, and returns the private key file path and the
// corresponding ssh.PublicKey.
func GenerateSSHKeypair(t *testing.T) (keyFile string, pubKey ssh.PublicKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// sandbox.OpenSession reads identityFile+".pub" to register the key.
	pubLine := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(keyPath+".pub", pubLine, 0o644); err != nil {
		t.Fatal(err)
	}

	return keyPath, sshPub
}

// NewSSHServer starts a TLS+SSH server that accepts connections authenticated
// with authorizedKey. The server is shut down automatically when the test ends.
func NewSSHServer(t *testing.T, authorizedKey ssh.PublicKey) *SSHServer {
	t.Helper()

	tlsCert := generateSelfSignedCert(t)
	hostSigner := generateHostKey(t)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatal(err)
	}

	sshCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, &unauthorizedKeyError{}
		},
	}
	sshCfg.AddHostKey(hostSigner)

	srv := &SSHServer{listener: listener}
	go srv.serve(sshCfg)
	t.Cleanup(func() { _ = listener.Close() })

	return srv
}

// Addr returns the "host:port" address the server is listening on.
func (s *SSHServer) Addr() string {
	return s.listener.Addr().String()
}

// SetResult configures the stdout output and exit code returned for exec requests.
func (s *SSHServer) SetResult(stdout string, exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stdout = stdout
	s.exitCode = exitCode
}

// Commands returns a copy of all exec command strings received so far.
func (s *SSHServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.commands))
	copy(out, s.commands)
	return out
}

func (s *SSHServer) serve(cfg *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn, cfg)
	}
}

func (s *SSHServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, requests)
	}
}

func (s *SSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()

	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}

		if len(req.Payload) < 4 {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		cmdLen := binary.BigEndian.Uint32(req.Payload[:4])
		if int(cmdLen) > len(req.Payload)-4 {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		cmd := string(req.Payload[4 : 4+cmdLen])

		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		stdout := s.stdout
		exitCode := s.exitCode
		s.mu.Unlock()

		if req.WantReply {
			_ = req.Reply(true, nil)
		}

		if stdout != "" {
			_, _ = ch.Write([]byte(stdout))
		}

		exitPayload := make([]byte, 4)
		binary.BigEndian.PutUint32(exitPayload, uint32(exitCode)) //nolint:gosec // exit codes are 0-255
		_, _ = ch.SendRequest("exit-status", false, exitPayload)
		return // one exec per session
	}
}

func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func generateHostKey(t *testing.T) ssh.Signer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

type unauthorizedKeyError struct{}

func (e *unauthorizedKeyError) Error() string { return "unauthorized key" }
