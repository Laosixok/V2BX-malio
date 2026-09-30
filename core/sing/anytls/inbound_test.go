package anytls

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anytls/sing-anytls/padding"
	"github.com/anytls/sing-anytls/session"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	singatomic "github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const testTimeout = 3 * time.Second

var testUsers = []option.AnyTLSUser{
	{Name: "alice", Password: "alice-secret"},
	{Name: "bob", Password: "bob-secret"},
}

type routedStream struct {
	conn net.Conn
	user string
	ctx  context.Context
}

type testRouter struct {
	adapter.Router
	routed chan routedStream
}

func (r *testRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.routed <- routedStream{conn: conn, user: metadata.User, ctx: ctx}
	go func() {
		_, err := io.Copy(conn, conn)
		_ = conn.Close()
		if onClose != nil {
			onClose(err)
		}
	}()
}

func newTestInbound(t *testing.T, tlsOptions *option.InboundTLSOptions) (*Inbound, *testRouter) {
	t.Helper()
	router := &testRouter{routed: make(chan routedStream, 16)}
	value, err := NewInbound(context.Background(), router, log.NewNOPFactory().Logger(), "test", option.AnyTLSInboundOptions{
		Users:                      testUsers,
		InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: tlsOptions},
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := value.(*Inbound)
	t.Cleanup(func() { closeInbound(t, inbound) })
	return inbound, router
}

type testConnection struct {
	peer      net.Conn
	done      chan struct{}
	callbacks atomic.Int32
}

func newTestConnection(t *testing.T, inbound *Inbound) *testConnection {
	t.Helper()
	peer, server := net.Pipe()
	if err := peer.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	connection := &testConnection{peer: peer, done: make(chan struct{})}
	t.Cleanup(func() { _ = peer.Close() })
	go func() {
		// This field is deliberately not the authenticated user. Deletion must
		// use the AnyTLS password authentication result, not input metadata.
		inbound.NewConnectionEx(context.Background(), server, adapter.InboundContext{User: "untrusted-context-user"}, func(error) {
			connection.callbacks.Add(1)
		})
		close(connection.done)
	}()
	return connection
}

func authentication(password string) []byte {
	hash := sha256.Sum256([]byte(password))
	message := make([]byte, 34)
	copy(message, hash[:])
	// The two-byte padding length is zero.
	return message
}

func authenticate(t *testing.T, inbound *Inbound, connection *testConnection, password, user string) {
	t.Helper()
	if _, err := connection.peer.Write(authentication(password)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connection to authenticate as "+user, func() bool {
		inbound.mutex.Lock()
		defer inbound.mutex.Unlock()
		for connection := range inbound.connections {
			if connection.authenticated && connection.user == user {
				return true
			}
		}
		return false
	})
}

func openTestStream(t *testing.T, router *testRouter, connection *testConnection) (*session.Session, *session.Stream, routedStream) {
	t.Helper()
	var factory singatomic.TypedValue[*padding.PaddingFactory]
	if !padding.UpdatePaddingScheme(padding.DefaultPaddingScheme, &factory) {
		t.Fatal("invalid default padding scheme")
	}
	client := session.NewClientSession(connection.peer, &factory, logger.NOP())
	client.Run()
	t.Cleanup(func() { _ = client.Close() })
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := M.SocksaddrSerializer.WriteAddrPort(stream, M.ParseSocksaddr("example.com:443")); err != nil {
		t.Fatal(err)
	}
	select {
	case routed := <-router.routed:
		return client, stream, routed
	case <-time.After(testTimeout):
		t.Fatal("stream was not routed")
		return nil, nil, routedStream{}
	}
}

func eventually(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(testTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("timed out waiting for " + description)
		}
	}
}

func waitDone(t *testing.T, connection *testConnection) {
	t.Helper()
	select {
	case <-connection.done:
	case <-time.After(testTimeout):
		t.Fatal("connection handler did not exit")
	}
	if got := connection.callbacks.Load(); got != 1 {
		t.Fatalf("parent close callback count = %d, want 1", got)
	}
}

func connectionCount(inbound *Inbound) int {
	inbound.mutex.Lock()
	defer inbound.mutex.Unlock()
	return len(inbound.connections)
}

func closeInbound(t *testing.T, inbound *Inbound) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- inbound.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("close inbound: %v", err)
		}
	case <-time.After(testTimeout):
		t.Error("inbound.Close did not return")
	}
}

func TestAuthenticatedEmptySessionsReleaseConnections(t *testing.T) {
	inbound, _ := newTestInbound(t, nil)
	// The old inbound attached deregistration to stream callbacks. These
	// authenticated sessions intentionally never create a stream, reproducing
	// the path that retained every completed TCP connection.
	for i := 0; i < 128; i++ {
		connection := newTestConnection(t, inbound)
		authenticate(t, inbound, connection, "alice-secret", "alice")
		_ = connection.peer.Close()
		waitDone(t, connection)
		if got := connectionCount(inbound); got != 0 {
			t.Fatalf("after session %d: %d retained connections", i, got)
		}
	}
}

func TestCloseTerminatesAuthenticationAndSessions(t *testing.T) {
	inbound, router := newTestInbound(t, nil)
	unauthenticated := newTestConnection(t, inbound)
	partial := newTestConnection(t, inbound)
	if _, err := partial.peer.Write(authentication("alice-secret")[:12]); err != nil {
		t.Fatal(err)
	}
	idle := newTestConnection(t, inbound)
	authenticate(t, inbound, idle, "alice-secret", "alice")
	active := newTestConnection(t, inbound)
	authenticate(t, inbound, active, "bob-secret", "bob")
	client, _, routed := openTestStream(t, router, active)
	if routed.user != "bob" {
		t.Fatalf("routed user = %q, want bob", routed.user)
	}
	eventually(t, "all four connections to register", func() bool { return connectionCount(inbound) == 4 })
	closeInbound(t, inbound)
	for _, connection := range []*testConnection{unauthenticated, partial, idle, active} {
		waitDone(t, connection)
	}
	if routed.ctx.Err() == nil {
		t.Fatal("session shutdown did not cancel pending route work")
	}
	eventually(t, "active client session to close", client.IsClosed)
	if got := connectionCount(inbound); got != 0 {
		t.Fatalf("Close retained %d connections", got)
	}
	// A connection racing listener shutdown must not escape the closed state.
	afterClose := newTestConnection(t, inbound)
	waitDone(t, afterClose)
}

func TestCloseTerminatesTLSHandshake(t *testing.T) {
	inbound, _ := newTestInbound(t, testTLSOptions(t))
	connection := newTestConnection(t, inbound)
	eventually(t, "TLS connection to register before handshake", func() bool { return connectionCount(inbound) == 1 })
	closeInbound(t, inbound)
	waitDone(t, connection)
	if got := connectionCount(inbound); got != 0 {
		t.Fatalf("TLS handshake retained %d connections", got)
	}
}

func TestTLSFragmentedAuthenticationAndStream(t *testing.T) {
	inbound, router := newTestInbound(t, testTLSOptions(t))
	connection := newTestConnection(t, inbound)
	// This in-memory peer uses the ephemeral certificate generated by this test.
	clientTLS := tls.Client(connection.peer, &tls.Config{InsecureSkipVerify: true})
	if err := clientTLS.Handshake(); err != nil {
		t.Fatal(err)
	}
	connection.peer = clientTLS
	header := authentication("alice-secret")
	header[33] = 3
	header = append(header, 1, 2, 3)
	for _, part := range [][]byte{header[:7], header[7:33], header[33:35], header[35:]} {
		if _, err := clientTLS.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	_, stream, routed := openTestStream(t, router, connection)
	if routed.user != "alice" {
		t.Fatalf("TLS stream user = %q", routed.user)
	}
	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(stream, reply); err != nil || string(reply) != "hello" {
		t.Fatalf("TLS echo = %q, error = %v", reply, err)
	}
	closeInbound(t, inbound)
	waitDone(t, connection)
}

func TestDelUsersClosesAuthenticatedSessionsOnly(t *testing.T) {
	inbound, router := newTestInbound(t, nil)
	alice := newTestConnection(t, inbound)
	authenticate(t, inbound, alice, "alice-secret", "alice")
	aliceClient, _, aliceRoute := openTestStream(t, router, alice)
	if aliceRoute.user != "alice" {
		t.Fatalf("routed user = %q, want alice", aliceRoute.user)
	}
	aliceIdle := newTestConnection(t, inbound)
	authenticate(t, inbound, aliceIdle, "alice-secret", "alice")
	bob := newTestConnection(t, inbound)
	authenticate(t, inbound, bob, "bob-secret", "bob")
	bobClient, bobStream, _ := openTestStream(t, router, bob)
	if err := inbound.DelUsers([]string{"alice"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, alice)
	waitDone(t, aliceIdle)
	eventually(t, "deleted user's client session to close", aliceClient.IsClosed)
	if got := connectionCount(inbound); got != 1 {
		t.Fatalf("remaining connection count = %d, want 1", got)
	}
	if bobClient.IsClosed() || bob.callbacks.Load() != 0 {
		t.Fatal("deleting alice closed bob's session")
	}
	const message = "still connected"
	if _, err := bobStream.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(message))
	if _, err := io.ReadFull(bobStream, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != message {
		t.Fatalf("echo = %q, want %q", reply, message)
	}
	// Completing one stream must not unregister its still-live parent session.
	if err := bobStream.Close(); err != nil {
		t.Fatal(err)
	}
	if bob.callbacks.Load() != 0 || connectionCount(inbound) != 1 {
		t.Fatal("stream close removed the live parent session")
	}
}

func TestAddUsersIsIdempotentAndPreservesPasswords(t *testing.T) {
	inbound, router := newTestInbound(t, nil)
	for i := 0; i < 256; i++ {
		if err := inbound.AddUsers(testUsers); err != nil {
			t.Fatal(err)
		}
	}
	inbound.mutex.Lock()
	userCount := len(inbound.users)
	passwordCount := len(inbound.usersByHash)
	inbound.mutex.Unlock()
	if userCount != 2 || passwordCount != 2 {
		t.Fatalf("repeated AddUsers retained %d users and %d passwords, want 2 each", userCount, passwordCount)
	}
	connection := newTestConnection(t, inbound)
	authenticate(t, inbound, connection, "alice-secret", "alice")
	_, _, routed := openTestStream(t, router, connection)
	if routed.user != "alice" {
		t.Fatalf("authenticated name = %q, want alice", routed.user)
	}
	rejected := newTestConnection(t, inbound)
	_, _ = rejected.peer.Write(authentication("alice"))
	waitDone(t, rejected)
	if err := inbound.DelUsers([]string{"alice"}); err != nil {
		t.Fatal(err)
	}
	bob := newTestConnection(t, inbound)
	authenticate(t, inbound, bob, "bob-secret", "bob")
	_, _, routed = openTestStream(t, router, bob)
	if routed.user != "bob" {
		t.Fatalf("remaining user name = %q, want bob", routed.user)
	}
}

func TestConcurrentAuthenticationAndUserUpdates(t *testing.T) {
	inbound, _ := newTestInbound(t, nil)
	const iterations = 64
	var workers sync.WaitGroup
	workers.Add(5)
	start := make(chan struct{})
	errors := make(chan error, 5)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			if err := inbound.AddUsers(testUsers); err != nil {
				errors <- err
				return
			}
			if err := inbound.DelUsers([]string{"bob"}); err != nil {
				errors <- err
				return
			}
		}
	}()
	for i := 0; i < 4; i++ {
		go func() {
			defer workers.Done()
			<-start
			for j := 0; j < iterations; j++ {
				peer, server := net.Pipe()
				_ = peer.SetDeadline(time.Now().Add(testTimeout))
				done := make(chan struct{})
				go func() {
					inbound.NewConnectionEx(context.Background(), server, adapter.InboundContext{}, nil)
					close(done)
				}()
				_, err := peer.Write(authentication("alice-secret"))
				_ = peer.Close()
				if err != nil {
					errors <- err
					return
				}
				select {
				case <-done:
				case <-time.After(testTimeout):
					errors <- context.DeadlineExceeded
					return
				}
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * testTimeout):
		t.Fatal("concurrent authentication and user updates did not finish")
	}
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if got := connectionCount(inbound); got != 0 {
		t.Fatalf("concurrent authentication retained %d connections", got)
	}
}

func testTLSOptions(t *testing.T) *option.InboundTLSOptions {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return &option.InboundTLSOptions{
		Enabled:     true,
		Certificate: []string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))},
		Key:         []string{string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))},
	}
}

func TestUnnamedConfiguredUsersRemainDistinct(t *testing.T) {
	inbound, _ := newTestInbound(t, nil)
	users := []option.AnyTLSUser{{Password: "first"}, {Password: "second"}}
	if err := inbound.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	if err := inbound.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	inbound.mutex.Lock()
	count := len(inbound.users)
	inbound.mutex.Unlock()
	if count != 4 {
		t.Fatalf("distinct named/unnamed users = %d, want 4", count)
	}
	for _, password := range []string{"first", "second"} {
		connection := newTestConnection(t, inbound)
		authenticate(t, inbound, connection, password, "")
		_ = connection.peer.Close()
		waitDone(t, connection)
	}
}
