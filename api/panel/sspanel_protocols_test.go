package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/InazumaV/V2bX/conf"
	"github.com/goccy/go-json"
)

const testVlessServer = "cdn.linkms001.store;443;0;ws;tls;path=/api/v1|server=hk09.linkms001.com|host=cdn-hk09.node021.top|sni=cdn-hk09.node021.top|security=tls|enable_vless=true"

func fixtureClient(t *testing.T, sort int, server string, nodeType string) *SSPanelClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "test" {
			t.Error("missing API key")
		}
		if r.URL.Path == "/mod_mu/users" {
			fmt.Fprint(w, `{"ret":1,"data":[{"id":1,"uuid":"stable-uuid","passwd":"ss-password","is_multi_user":0},{"id":2,"uuid":"carrier","passwd":"carrier-password","is_multi_user":1}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(SSPanelNodeResponse{Ret: 1, Data: SSPanelNodeData{Sort: sort, Server: server}})
	}))
	t.Cleanup(srv.Close)
	c, err := NewSSPanel(&conf.ApiConfig{APIHost: srv.URL, Key: "test", NodeID: 1, NodeType: nodeType, SSPort: 22222, SSMethod: "aes-128-gcm"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSSPanelVlessWS(t *testing.T) {
	c := fixtureClient(t, 15, testVlessServer, "vless")
	n, err := c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Security != Tls || n.VAllss.Network != "ws" || n.Common.ServerPort != 443 {
		t.Fatalf("wrong node: %+v", n)
	}
	var ws struct {
		Path    string
		Headers map[string]string
	}
	if err = json.Unmarshal(n.VAllss.NetworkSettings, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.Path != "/api/v1" || ws.Headers["Host"] != "cdn-hk09.node021.top" || n.VAllss.TlsSettings.ServerName != "cdn-hk09.node021.top" {
		t.Fatalf("lost WS/TLS settings: %+v %+v", ws, n.VAllss.TlsSettings)
	}
	next, err := c.GetNodeInfo()
	if err != nil || next != nil {
		t.Fatal("unchanged node must not rebuild", err)
	}
}

func TestSSPanelVlessFormats(t *testing.T) {
	for _, input := range []string{
		"origin.example;8443;0;ws;tls;path=/ws?token=a=b&ed=2048|host=ws.example|sni=tls.example",
		"origin.example;port=8443&net=ws&security=tls&path=/ws&host=ws.example&sni=tls.example",
	} {
		_, p, err := parseVlessServer(input)
		if err != nil {
			t.Fatal(err)
		}
		if p["net"] != "ws" || p["serverName"] != "tls.example" || p["port"] != "8443" {
			t.Fatal(p)
		}
	}
	_, p, err := parseVlessServer("origin;443;0;ws;tls;path=/ws?token=a=b&ed=2048|host=ws.example")
	if err != nil || p["path"] != "/ws?token=a=b&ed=2048" {
		t.Fatal("path damaged", p, err)
	}
	for _, bad := range []string{"origin;abc;0;ws;tls", "origin;port=70000", "origin;443;0;kcp;tls"} {
		if _, _, err := parseVlessServer(bad); err == nil {
			t.Fatal("accepted invalid config", bad)
		}
	}
}

func TestSSPanelRealityPreserved(t *testing.T) {
	c := fixtureClient(t, 16, "origin;port=443&security=reality&serverName=www.example.com&privateKey=example-key=&shortId=abcd&flow=xtls-rprx-vision", "vless")
	n, err := c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Security != Reality || n.VAllss.TlsSettings.PrivateKey != "example-key=" || n.VAllss.Flow != "xtls-rprx-vision" {
		t.Fatal("Reality changed", n)
	}
}

func TestSSPanelShadowsocks(t *testing.T) {
	for _, sort := range []int{0, 10} {
		c := fixtureClient(t, sort, "hk09.linkms001.com", "shadowsocks")
		n, err := c.GetNodeInfo()
		if err != nil {
			t.Fatal(err)
		}
		if n.Common.ServerPort != 22222 || n.Shadowsocks.Cipher != "aes-128-gcm" || n.Type != "shadowsocks" {
			t.Fatal(n)
		}
		users, err := c.GetUserList()
		if err != nil {
			t.Fatal(err)
		}
		if len(users) != 1 || users[0].Uuid != "stable-uuid" || users[0].ShadowsocksPassword() != "ss-password" {
			t.Fatal("SS credentials/carrier filtering", users)
		}
	}
}

func TestSSPanelInvalidNodeNotCached(t *testing.T) {
	c := fixtureClient(t, 0, "origin", "shadowsocks")
	c.ssPort = 0
	for i := 0; i < 2; i++ {
		if _, err := c.GetNodeInfo(); err == nil {
			t.Fatal("invalid config was accepted/cached")
		}
	}
	c.ssPort = 22222
	if _, err := c.GetNodeInfo(); err != nil {
		t.Fatal(err)
	}
}

func TestSSPanelVlessValidation(t *testing.T) {
	for _, tc := range []struct {
		sort             int
		server, nodeType string
	}{
		{15, "origin;443;0;ws;tls;flow=xtls-rprx-vision", "vless"},
		{16, "origin;port=443&net=ws&privateKey=key", "vless"},
		{16, "origin;port=443&security=reality", "vless"},
		{15, testVlessServer, "vmess"},
		{0, "origin", "vless"},
	} {
		c := fixtureClient(t, tc.sort, tc.server, tc.nodeType)
		if _, err := c.GetNodeInfo(); err == nil {
			t.Fatalf("accepted invalid node: %+v", tc)
		}
	}
}

func TestSSPanelVlessGRPCAndPlain(t *testing.T) {
	c := fixtureClient(t, 15, "origin;8443;0;grpc;tls;serviceName=example|sni=tls.example", "vless")
	n, err := c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]string
	if err = json.Unmarshal(n.VAllss.NetworkSettings, &settings); err != nil {
		t.Fatal(err)
	}
	if n.VAllss.Network != "grpc" || settings["serviceName"] != "example" {
		t.Fatal(n, settings)
	}
	c = fixtureClient(t, 15, "origin;8080;0;ws;none;path=/ws", "vless")
	n, err = c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Security != None || n.Common.ServerPort != 8080 {
		t.Fatal(n)
	}
}

func TestSSPanelAutomaticShadowsocks(t *testing.T) {
	data := SSPanelNodeData{Sort: 0, Server: "origin.example", SSPort: 22222, SSMethod: "aes-128-gcm"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SSPanelNodeResponse{Ret: 1, Data: data})
	}))
	defer srv.Close()
	c, err := NewSSPanel(&conf.ApiConfig{APIHost: srv.URL, Key: "test", NodeID: 20, NodeType: "shadowsocks"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Common.ServerPort != 22222 || n.Shadowsocks.Cipher != "aes-128-gcm" {
		t.Fatal(n)
	}
	if n, err = c.GetNodeInfo(); err != nil || n != nil {
		t.Fatal("unchanged config rebuilt", err)
	}
	data.SSPort = 24444
	data.SSMethod = "chacha20-ietf-poly1305"
	n, err = c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Common.ServerPort != 24444 || n.Shadowsocks.Cipher != data.SSMethod {
		t.Fatal("panel changes not detected", n)
	}
	data.SSConfigError = "ambiguous carrier"
	if _, err = c.GetNodeInfo(); err == nil {
		t.Fatal("invalid panel config accepted")
	}
	c.ssPort, c.ssMethod = 33333, "aes-256-gcm"
	n, err = c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if n.Common.ServerPort != 33333 || n.Shadowsocks.Cipher != "aes-256-gcm" {
		t.Fatal("manual override ignored", n)
	}
}
