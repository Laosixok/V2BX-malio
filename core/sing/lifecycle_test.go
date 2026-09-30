package sing

import (
	"context"
	"crypto/sha256"
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/core/sing/anytls"
	"github.com/sagernet/sing-box/adapter"
)

// Exercise the production registry and manager, not only the isolated adapter:
// every reload must terminate its old session and remove its traffic cache.
func TestSingAnyTLSReload(t *testing.T) {
	value, err := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	s := value.(*Sing)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{}, AnyTls: &panel.AnyTlsNode{}}
	options := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}
	for iteration := 0; iteration < 32; iteration++ {
		if err := s.AddNode("reload", info, options); err != nil {
			t.Fatal(err)
		}
		in, ok := s.box.Inbound().Get("reload")
		if !ok {
			t.Fatal("inbound not registered")
		}
		if _, ok := in.(*anytls.Inbound); !ok {
			t.Fatalf("wrong AnyTLS registration: %T", in)
		}
		if _, err := s.AddUsers(&core.AddUsersParams{Tag: "reload", NodeInfo: info, Users: []panel.UserInfo{{Uuid: "secret"}}}); err != nil {
			t.Fatal(err)
		}
		peer, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			in.(adapter.ConnectionHandlerEx).NewConnectionEx(context.Background(), server, adapter.InboundContext{}, nil)
			close(done)
		}()
		_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
		hash := sha256.Sum256([]byte("secret"))
		header := append(hash[:], 0, 0)
		if _, err := peer.Write(header); err != nil {
			_ = peer.Close()
			t.Fatal(err)
		}
		s.hookServer.getTrafficCounter("reload").Tx("secret", 1)
		removed := make(chan error, 1)
		go func() { removed <- s.DelNode("reload") }()
		select {
		case err := <-removed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			_ = peer.Close()
			t.Fatal("DelNode blocked")
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = peer.Close()
			t.Fatal("reload retained old session")
		}
		_ = peer.Close()
		if _, ok := s.box.Inbound().Get("reload"); ok {
			t.Fatal("reload retained registered inbound")
		}
		if _, ok := s.hookServer.counter.Load("reload"); ok {
			t.Fatal("reload retained traffic cache")
		}
	}
}
