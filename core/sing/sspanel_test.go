package sing

import (
	"context"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/core"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"testing"
	"time"
)

func TestSSPanelVlessTransport(t *testing.T) {
	n := &panel.NodeInfo{Type: "vless", Security: panel.Tls, Common: &panel.CommonNode{ServerPort: 443}, VAllss: &panel.VAllssNode{Network: "ws", NetworkSettings: []byte(`{"path":"/api/v1","headers":{"Host":"cdn-hk09.node021.top"}}`)}}
	c := &conf.Options{ListenIP: "0.0.0.0", SingOptions: conf.NewSingOptions(), CertConfig: &conf.CertConfig{CertMode: "file", CertFile: "cert.pem", KeyFile: "key.pem"}}
	in, err := getInboundOptions("test", n, c)
	if err != nil {
		t.Fatal(err)
	}
	v := in.Options.(*option.VLESSInboundOptions)
	if !v.TLS.Enabled || v.TLS.CertificatePath != "cert.pem" || v.Transport.Type != "ws" || v.Transport.WebsocketOptions.Path != "/api/v1" || v.ListenPort != 443 {
		t.Fatalf("lost WS/TLS settings: %+v", v)
	}
}

// Exercise the production user loader and an encrypted SS connection. The
// authentication password deliberately differs from the accounting UUID.
func TestSSPanelPasswordConnection(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	value, err := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	s := value.(*Sing)
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, cipher := range []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"} {
		t.Run(cipher, func(t *testing.T) {
			info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{}, Shadowsocks: &panel.ShadowsocksNode{Cipher: cipher}}
			if err := s.AddNode(cipher, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
				t.Fatal(err)
			}
			defer s.DelNode(cipher)
			user := panel.UserInfo{Id: 1, Uuid: "stable-uuid", Password: "first-password"}
			for _, password := range []string{"first-password", "rotated-password"} {
				if password != user.Password {
					if err := s.DelUsers([]panel.UserInfo{user}, cipher, info); err != nil {
						t.Fatal(err)
					}
					user.Password = password
				}
				if _, err := s.AddUsers(&core.AddUsersParams{Tag: cipher, NodeInfo: info, Users: []panel.UserInfo{user}}); err != nil {
					t.Fatal(err)
				}
				in, _ := s.box.Inbound().Get(cipher)
				peer, server := net.Pipe()
				go in.(adapter.ConnectionHandlerEx).NewConnectionEx(context.Background(), server, adapter.InboundContext{}, nil)
				peer.SetDeadline(time.Now().Add(3 * time.Second))
				method, err := shadowaead.New(cipher, nil, password)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := method.DialConn(peer, M.ParseSocksaddr(echo.Addr().String()))
				if err != nil {
					peer.Close()
					t.Fatal(err)
				}
				payload := []byte("SSPanel authentication roundtrip")
				if _, err = conn.Write(payload); err != nil {
					conn.Close()
					t.Fatal(err)
				}
				got := make([]byte, len(payload))
				_, err = io.ReadFull(conn, got)
				conn.Close()
				if err != nil || string(got) != string(payload) {
					t.Fatalf("password roundtrip failed: %q %v", got, err)
				}
			}
		})
	}
}
