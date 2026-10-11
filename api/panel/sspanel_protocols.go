package panel

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
)

// parseVlessServer accepts the panel's positional format and the Reality API's
// host;key=value&key=value format. SplitN preserves '=' inside paths and keys.
func parseVlessServer(server string) (string, map[string]string, error) {
	parts := strings.Split(server, ";")
	if strings.TrimSpace(parts[0]) == "" {
		return "", nil, fmt.Errorf("empty VLESS server")
	}
	params := make(map[string]string)
	start := 1
	if len(parts) >= 5 && !strings.Contains(parts[1], "=") {
		params["port"], params["net"], params["security"] = parts[1], parts[3], parts[4]
		start = 5
	}
	for _, section := range parts[start:] {
		separator := "&"
		if strings.Contains(section, "|") {
			separator = "|"
		}
		for _, pair := range strings.Split(section, separator) {
			if pair == "" {
				continue
			}
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				return "", nil, fmt.Errorf("invalid VLESS parameter (expected key=value)")
			}
			params[kv[0]] = kv[1]
		}
	}
	if params["port"] == "" {
		params["port"] = "443"
	}
	port, err := strconv.Atoi(params["port"])
	if err != nil || port < 1 || port > 65535 {
		return "", nil, fmt.Errorf("invalid VLESS listen port")
	}
	if params["net"] == "" {
		params["net"] = "tcp"
	}
	if params["net"] == "raw" {
		params["net"] = "tcp"
	}
	switch params["net"] {
	case "tcp", "ws", "grpc", "httpupgrade":
	default:
		return "", nil, fmt.Errorf("unsupported VLESS transport: %s", params["net"])
	}
	if params["security"] == "" {
		params["security"] = params["tls"]
	}
	switch params["security"] {
	case "", "none", "tls", "reality":
	default:
		return "", nil, fmt.Errorf("unsupported VLESS security: %s", params["security"])
	}
	if params["serverName"] == "" {
		params["serverName"] = params["sni"]
	}
	if params["serverName"] == "" {
		params["serverName"] = params["host"]
	}
	return parts[0], params, nil
}

func vlessNetworkSettings(params map[string]string) json.RawMessage {
	var settings interface{}
	switch params["net"] {
	case "ws":
		path := params["path"]
		if path == "" {
			path = "/"
		}
		settings = map[string]interface{}{"path": path, "headers": map[string]string{"Host": params["host"]}}
	case "grpc":
		service := params["serviceName"]
		if service == "" {
			service = params["servicename"]
		}
		settings = map[string]string{"serviceName": service}
	case "httpupgrade":
		settings = map[string]string{"path": params["path"], "host": params["host"]}
	default:
		return nil
	}
	data, _ := json.Marshal(settings)
	return data
}

// Legacy SSPanel sends only the origin host for sort 0/10; the public port
// mapping belongs to the client, not the backend listener. Require explicit
// backend settings rather than guessing from a user's port or defaulting to 443.
func (c *SSPanelClient) createShadowsocksNode(node *NodeInfo, host string) (*NodeInfo, error) {
	if c.ssPort < 1 || c.ssPort > 65535 {
		return nil, fmt.Errorf("SSPanel Shadowsocks requires ApiConfig.SSPort (backend listen port, e.g. 22222, not public mapped port)")
	}
	switch c.ssMethod {
	case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
	default:
		return nil, fmt.Errorf("SSPanel Shadowsocks requires ApiConfig.SSMethod: aes-128-gcm, aes-256-gcm or chacha20-ietf-poly1305; SSR/plugins and SS2022 are not this adapter's AEAD mode")
	}
	node.Type = "shadowsocks"
	node.Security = None
	node.Shadowsocks = &ShadowsocksNode{CommonNode: CommonNode{Host: host, ServerPort: c.ssPort, BaseConfig: &BaseConfig{PushInterval: "60s", PullInterval: "60s"}}, Cipher: c.ssMethod}
	return node, nil
}
