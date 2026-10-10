# SSPanel / C20 的 SS 与 VLESS 适配

基于 Laosixok/V2BX-malio 1.0.7（2ce896b），保留该版 AnyTLS 会话、流量缓存清理和安装回退改动；不升级内核依赖。本补丁不需要覆盖面板文件。

## VLESS + WS + TLS

面板节点类型使用 VLESS（sort=15），地址可以保持：

```text
cdn.linkms001.store;443;0;ws;tls;path=/api/v1|server=hk09.linkms001.com|host=cdn-hk09.node021.top|sni=cdn-hk09.node021.top|security=tls|enable_vless=true
```

后端解析为 VLESS / WS / 443，路径 `/api/v1`，Host 与 SNI `cdn-hk09.node021.top`。`server=` 是面板的地址覆写参数，不是后端绑定地址；后端绑定由 `ListenIP` 控制。支持旧式 `host;port=443&net=ws&security=tls&path=/api/v1` 格式。Reality 仍使用 sort=16、TCP、privateKey 等原有配置；WS 不设置 Vision flow。

合并以下节点到现有 `Nodes`，替换面板地址、密钥和 NodeID；现有 `Cores` 必须包含 xray。也可将 Core 改成 sing 并配置 sing 内核。

```json
{
  "ApiConfig": {
    "PanelType": "sspanel",
    "ApiHost": "https://panel.example.com",
    "ApiKey": "替换为面板 muKey",
    "NodeID": 15,
    "NodeType": "vless"
  },
  "Options": {
    "Core": "xray",
    "ListenIP": "0.0.0.0",
    "CertConfig": {
      "CertMode": "file",
      "CertFile": "/etc/V2bX/cert.pem",
      "KeyFile": "/etc/V2bX/key.pem"
    }
  }
}
```

`tls` 参数声明传输类型，不会自动生成证书。如果 V2bX 自己终止 TLS，必须配置有效证书和私钥。客户端直连时证书要匹配客户端 SNI；经过 CDN 时也要满足 CDN 对回源证书的校验要求。如果由同机 Nginx 终止 TLS、向后端转发明文 WS，可以显式使用 `CertMode: none`，并安排不冲突的后端监听端口与反代规则。默认 `none` 不会在 V2bX 开启 TLS。

## Shadowsocks AEAD 单端口多用户

面板配置：

```text
hk09.linkms001.com;server=cn2.hk.linkms001.store|port=22222#29346
```

客户端连接 `cn2.hk.linkms001.store:29346`；入口转发到后端实际监听的 `22222`。后端不会创建这条转发。

当前面板 `/mod_mu/nodes/{id}/info` 对 sort=0/10 只下发第一个主机名，不包含端口映射和加密方式。因此新增显式配置 `SSPort` / `SSMethod`，不能直接从此接口自动还原。`SSMethod` 必须与面板单端口承载用户的加密方式一致；以下 aes-128-gcm 只是示例。

```json
{
  "ApiConfig": {
    "PanelType": "sspanel",
    "ApiHost": "https://panel.example.com",
    "ApiKey": "替换为面板 muKey",
    "NodeID": 10,
    "NodeType": "shadowsocks",
    "SSPort": 22222,
    "SSMethod": "aes-128-gcm"
  },
  "Options": {
    "Core": "xray",
    "ListenIP": "0.0.0.0"
  }
}
```

适配要求：

- 面板 SS 节点 sort=0/10，使用单端口订阅；承载用户端口为 22222。
- 承载用户配置 `protocol=origin`、`obfs=plain`，算法为 `aes-128-gcm`、`aes-256-gcm` 或 `chacha20-ietf-poly1305`。
- 后端使用每个真实用户的 `passwd` 验证，使用 UUID 关联流量与限速；跳过承载账号。密码修改会触发删除旧账号并加载新密码。
- 这不是每用户独立监听端口、SSR 混淆协议、节点固定共享密码模式或 SS2022 适配。上述模式不能只换本后端而保持原订阅不变。
- 如果面板还输出普通多端口订阅，需将该节点设置为只提供相应的单端口订阅，避免客户端选到本后端未监听的端口。

## 升级与回退

使用匹配架构的补丁二进制；不要覆盖 `/usr/bin/V2bX` 管理脚本，也不要直接把发行 ZIP 解压到配置目录。先备份实际运行核心和 `/etc/V2bX`，停止服务后替换核心，保留原配置、证书和规则文件，再加入上面的节点配置。重启后检查日志、用户数量和实际订阅连接。失败时恢复备份核心与配置。

补丁构建版本为 `1.0.8`。构建：

```sh
VERSION=1.0.8 bash build.sh
```

真实 CDN 回源、服务器证书和入口转发不在本地测试范围，部署后需实际连接验证。内存观察方法继续使用 `docs/anytls-memory-fix.md`。
