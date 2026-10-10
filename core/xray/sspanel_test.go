package xray

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/xtls/xray-core/app/proxyman"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSSPanelPassword(t *testing.T) {
	u := buildSSUser("test", &panel.UserInfo{Uuid: "stable-identity", Password: "actual-ss-password"}, "aes-128-gcm", "")
	account, err := u.Account.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	if account.(*shadowsocks.Account).Password != "actual-ss-password" {
		t.Fatal("used UUID as password")
	}
	if u.Email == "" {
		t.Fatal("missing accounting identity")
	}
}
func TestSSPanelVlessTransport(t *testing.T) {
	node := &panel.NodeInfo{Type: "vless", VAllss: &panel.VAllssNode{Network: "ws", NetworkSettings: []byte(`{"path":"/api/v1","headers":{"Host":"cdn-hk09.node021.top"}}`)}}
	in := &coreConf.InboundDetourConfig{}
	if err := buildV2ray(&conf.Options{XrayOptions: conf.NewXrayOptions()}, node, in); err != nil {
		t.Fatal(err)
	}
	if in.Protocol != "vless" || in.StreamSetting.WSSettings.Path != "/api/v1" {
		t.Fatal("lost VLESS WS transport")
	}
}

func TestSSPanelVlessTLSInbound(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"cdn-hk09.node021.top"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	n := &panel.NodeInfo{Type: "vless", Security: panel.Tls, Common: &panel.CommonNode{ServerPort: 443}, VAllss: &panel.VAllssNode{Network: "ws", NetworkSettings: []byte(`{"path":"/api/v1","headers":{"Host":"cdn-hk09.node021.top"}}`)}}
	o := &conf.Options{ListenIP: "127.0.0.1", XrayOptions: conf.NewXrayOptions(), CertConfig: &conf.CertConfig{CertMode: "file", CertFile: certPath, KeyFile: keyPath}}
	inbound, err := buildInbound(o, n, "test")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := inbound.ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	receiver := settings.(*proxyman.ReceiverConfig)
	if receiver.StreamSettings.SecurityType != "xray.transport.internet.tls.Config" {
		t.Fatalf("TLS disabled: %+v", receiver.StreamSettings)
	}
}
