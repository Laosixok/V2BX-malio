// SPDX-License-Identifier: GPL-3.0-or-later
// The inbound adapter is based on sing-box's protocol/anytls/inbound.go
// (Copyright (C) 2022 by nekohasekai <contact-sagernet@sekai.icu>), as
// extended by wyx2685/sing-box_mod v1.12.0-beta.17.2. The session protocol
// is provided by github.com/anytls/sing-anytls (Copyright (C) 2025 anytls).
// Both upstream projects are licensed under GPL-3.0-or-later.

package anytls

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/anytls/sing-anytls/padding"
	"github.com/anytls/sing-anytls/session"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewInbound)
}

type serverConnection struct {
	conn          net.Conn
	done          chan struct{}
	user          string
	authenticated bool
}

// Panel users are keyed by name. Original sing-box configurations may omit
// names, in which case distinct passwords must remain distinct users.
type userKey struct {
	name            string
	unnamedPassword string
}

func keyForUser(user option.AnyTLSUser) userKey {
	if user.Name == "" {
		return userKey{unnamedPassword: user.Password}
	}
	return userKey{name: user.Name}
}

type Inbound struct {
	inbound.Adapter
	tlsConfig tls.ServerConfig
	router    adapter.ConnectionRouterEx
	logger    logger.ContextLogger
	listener  *listener.Listener
	padding   atomic.TypedValue[*padding.PaddingFactory]

	// Authentication and user deletion share this lock: a connection cannot
	// authenticate with an old user snapshot after DelUsers has selected the
	// sessions it needs to close. Network operations always happen outside it.
	mutex       sync.Mutex
	users       map[userKey]option.AnyTLSUser
	usersByHash map[[32]byte]string
	connections map[*serverConnection]struct{}
	closed      bool
	closeDone   chan struct{}
	closeErr    error
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	h := &Inbound{
		Adapter:     inbound.NewAdapter(C.TypeAnyTLS, tag),
		router:      uot.NewRouter(router, logger),
		logger:      logger,
		users:       make(map[userKey]option.AnyTLSUser),
		usersByHash: make(map[[32]byte]string),
		connections: make(map[*serverConnection]struct{}),
		closeDone:   make(chan struct{}),
	}
	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}
	if !padding.UpdatePaddingScheme(paddingScheme, &h.padding) {
		return nil, errors.New("incorrect padding scheme format")
	}
	if options.TLS != nil && options.TLS.Enabled {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		h.tlsConfig = tlsConfig
	}
	h.addUsers(options.Users)
	h.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: h,
	})
	return h, nil
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	h.mutex.Lock()
	if h.closed {
		h.mutex.Unlock()
		<-h.closeDone
		return h.closeErr
	}
	h.closed = true
	connections := make([]*serverConnection, 0, len(h.connections))
	for connection := range h.connections {
		connections = append(connections, connection)
	}
	h.mutex.Unlock()

	err := h.listener.Close()
	closeConnections(connections)
	err = E.Errors(err, common.Close(h.tlsConfig))
	h.mutex.Lock()
	h.closeErr = err
	close(h.closeDone)
	h.mutex.Unlock()
	return err
}

// closeConnections first interrupts every socket, including TLS handshakes,
// then waits for all session handlers to release their registry entries.
func closeConnections(connections []*serverConnection) {
	for _, connection := range connections {
		_ = connection.conn.Close()
	}
	for _, connection := range connections {
		<-connection.done
	}
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	connection := &serverConnection{conn: conn, done: make(chan struct{})}
	h.mutex.Lock()
	if h.closed {
		h.mutex.Unlock()
		N.CloseOnHandshakeFailure(conn, onClose, net.ErrClosed)
		return
	}
	h.connections[connection] = struct{}{}
	h.mutex.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	var connectionErr error
	defer func() {
		// Abort pending route/dial work as well as closing the session socket.
		cancel()
		_ = connection.conn.Close()
		h.mutex.Lock()
		delete(h.connections, connection)
		close(connection.done)
		h.mutex.Unlock()
		// This callback belongs to the TCP session, not its individual streams.
		if onClose != nil {
			onClose(connectionErr)
		}
	}()
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			connectionErr = err
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	connectionErr = h.serveConnection(ctx, conn, connection, metadata)
	if connectionErr != nil {
		h.logger.ErrorContext(ctx, E.Cause(connectionErr, "process connection from ", metadata.Source))
	}
}

func (h *Inbound) serveConnection(ctx context.Context, conn net.Conn, connection *serverConnection, metadata adapter.InboundContext) error {
	// Read exact fields: TCP/TLS record boundaries need not coincide with
	// the authentication header, and remaining bytes belong to the session.
	var passwordHash [sha256.Size]byte
	if _, err := io.ReadFull(conn, passwordHash[:]); err != nil {
		return err
	}
	h.mutex.Lock()
	user, exists := h.usersByHash[passwordHash]
	if h.closed || !exists {
		h.mutex.Unlock()
		return errors.New("unknown user password or closed inbound")
	}
	connection.user = user
	connection.authenticated = true
	h.mutex.Unlock()

	var paddingLength [2]byte
	if _, err := io.ReadFull(conn, paddingLength[:]); err != nil {
		return E.Extend(err, "read padding length")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint16(paddingLength[:]))); err != nil {
		return E.Extend(err, "read padding")
	}
	ctx = auth.ContextWithUser(adapter.WithContext(ctx, &metadata), user)
	serverSession := session.NewServerSession(conn, func(stream *session.Stream) {
		destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
		if err != nil {
			_ = stream.Close()
			h.logger.ErrorContext(ctx, E.Cause(err, "read stream destination"))
			return
		}
		(*inboundHandler)(h).NewConnectionEx(ctx, stream, metadata.Source, destination, nil)
	}, &h.padding, h.logger)
	defer serverSession.Close()
	serverSession.Run()
	return nil
}

// addUsers is called with mutex held, except during construction.
func (h *Inbound) addUsers(users []option.AnyTLSUser) {
	for _, user := range users {
		key := keyForUser(user)
		if previous, exists := h.users[key]; exists {
			previousHash := sha256.Sum256([]byte(previous.Password))
			if h.usersByHash[previousHash] == user.Name {
				delete(h.usersByHash, previousHash)
			}
		}
		h.users[key] = user
		h.usersByHash[sha256.Sum256([]byte(user.Password))] = user.Name
	}
}

func (h *Inbound) AddUsers(users []option.AnyTLSUser) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	h.addUsers(users)
	return nil
}

func (h *Inbound) DelUsers(names []string) error {
	h.mutex.Lock()
	toDelete := make(map[string]struct{}, len(names))
	for _, name := range names {
		toDelete[name] = struct{}{}
	}
	for key, user := range h.users {
		if _, exists := toDelete[user.Name]; exists {
			passwordHash := sha256.Sum256([]byte(user.Password))
			if h.usersByHash[passwordHash] == user.Name {
				delete(h.usersByHash, passwordHash)
			}
			delete(h.users, key)
		}
	}
	var connections []*serverConnection
	for connection := range h.connections {
		if _, exists := toDelete[connection.user]; connection.authenticated && exists {
			connections = append(connections, connection)
		}
	}
	h.mutex.Unlock()
	closeConnections(connections)
	return nil
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.InboundOptions = h.listener.ListenOptions().InboundOptions
	metadata.Source = source
	metadata.Destination = destination.Unwrap()
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
