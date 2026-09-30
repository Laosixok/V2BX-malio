# AnyTLS 内存持续增长：修复与验证

## 修复范围和证据

基于 `myTAT-boss/V2BX` 的 `41a19ab`。以下是源码可确认的资源生命周期缺陷；没有服务器堆快照，不能断言它们解释了生产服务器全部 RSS 增长，也未完成真实负载 24–48 小时观察。

- 锁定的 sing-box_mod AnyTLS 入站在认证前把 TCP 连接放进 `userconns`，只通过传给各条 stream 的回调删除。已认证、没有创建 stream 的会话正常结束时，没有回调负责清除该连接。反过来，单条 stream 结束又会过早移除仍在工作的整个 session。
- 该入站的 `Close()` 只关闭监听器和 TLS 配置，已有 session 继续存活并持有旧 inbound。`DelUsers` 用认证前通常为空的 `metadata.User` 匹配用户名，无法可靠关闭目标用户的 session。
- `AddUsers` 无条件追加 UUID，重复添加会增加保留数据；上游 `Service.UpdateUsers` 与认证读取无同步。
- Sing 流量缓存没有在用户真正消失或节点移除时删除；并发首次连接还可能创建不同计数器。现在使用 `LoadOrStore`，通过可选清理接口删除历史缓存。同 UUID 的限速/设备数修改保留未上报流量。
- 节点修改拉取周期时，在定时任务自身回调中调用自身 `Close()`，导致自锁；现改为让回调返回后按新周期排期。另修正旧 limiter 删除顺序、推送周期误用拉取周期，以及 200 空用户列表应清空用户而 304 应保留用户的区别。

使用原锁定的 sing-box_mod 和 sing-anytls 依赖运行最小 net.Pipe 复现实验：空会话结束时上层关闭回调计数为 0；调用入站 Close 后，已认证会话仍运行，直到主动关闭客户端。这证实了前两条缺陷。修复版对等测试会要求回调恰好一次、关闭后处理函数退出且连接注册表归零。

正常用户轮询本来采用差量更新，并非每次都重新添加全部用户。inbound manager 的正常 Remove 也会移除注册条目；本次针对其下层未结束的连接和缓存引用修复。

## 依赖与兼容性

| 模块 | 修改前 | 修改后 |
|---|---|---|
| sing-box 的实际 replace | wyx2685/sing-box_mod v1.12.0-beta.17.2 | 保持不变 |
| anytls/sing-anytls | v0.0.9-0.20250508103614-8bc6dd599731 | v0.0.13 |

在 `core/sing/anytls` 注册本地入站，仍使用 sing-box 的 TLS、监听器、路由、UoT 和原配置类型；仍使用 sing-anytls 的 padding/session/stream 实现。用户鉴权、socket 归属与关闭路径由本地代码管理。用户删除与认证共用锁，网络关闭在锁外进行；会话退出取消路由上下文，断开全部 stream，并清理 TCP 引用。

没有变更 SSPanel/Malio API 地址或请求格式、其他协议的入站实现、配置字段。新增的流量清理接口为可选接口，其他内核无需实现。AnyTLS 用户删除/节点重载会真正断开原连接，客户端需要重连，这是修复后的预期行为。

上游依据：

- [锁定入站源码](https://github.com/wyx2685/sing-box_mod/blob/v1.12.0-beta.17.2/protocol/anytls/inbound.go)
- [FIN/关闭处理修复](https://github.com/anytls/sing-anytls/commit/130d2e61b8895727bfed4942c535e91b246a9603)
- [Close 主动解除阻塞 I/O](https://github.com/anytls/sing-anytls/commit/7e275ba4e0a4b96fd7dcc23da650d755a9571acf)
- [选用的 v0.0.13](https://github.com/anytls/sing-anytls/tree/v0.0.13)

`sing-anytls` 没有 v0.0.10 tag；此前对话中混用了 anytls-go 与 sing-anytls 的发布记录，应以此处实际模块源码为准。

## 构建与测试

使用仓库指定的 Go 1.24.1。发布需启用 sing 等构建标签；README 中裸 `go build main.go` 不等于包含 AnyTLS 内核的完整发布构建。

```sh
go mod tidy
go test -timeout 30s ./...
go test -race -count=5 ./core/sing/anytls
go test -race ./core ./node -run 'Test(Monitor|SelectorDeleteUserTraffic)'
# 本次 macOS 主机的完整测试采用与 Linux 发布相同的无 CGO 模式：
CGO_ENABLED=0 go test -timeout 60s -skip '^(TestConf_Watch|TestLego_CreateCertByDns|TestLego_RenewCert)$' ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -tags 'sing,xray,hysteria2,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api' \
  -o build/V2bX-amd64 main.go
```

arm64 服务器把 GOARCH 和输出名称改为 arm64。此构建复用 `build.sh` 的协议标签，无需重新下载或替换服务器已有的 GeoIP/GeoSite 和配置文件。

本次已执行的验证（macOS amd64 宿主，Go 1.24.1）：

- `go mod tidy`：通过；最初遇到 DNS/下载 EOF，重试成功。最终只有 sing-anytls 的版本及两条校验和变化。
- AnyTLS 所有回归测试 `-race -count=5`：通过。覆盖空会话、TLS 握手中断、真实 TLS/分段认证/流量往返、删用户、stream 关闭不误删 session、重复添加、未设置名字的原生配置用户及并发更新。
- Sing 注册/manager 连续 32 次节点重载、用户/节点缓存更替和 Selector 清理委派：通过。
- SSPanel HTTP 测试：限速变化保留流量、真删除清缓存、200 空列表清空、304 保留、删除失败不清缓存、节点重载及不同 push/pull 周期均通过。
- 原样完整测试（加 `CGO_ENABLED=0` 和 30 秒超时）：失败。原有 `TestConf_Watch` 包含 `select {}` 永久阻塞而超时；`TestLego_CreateCertByDns` 用占位 Cloudflare token `123`，返回 6003 无效请求头。还暴露出旧 `fmt.Errorf(errorString)` 无常量格式的 Go 1.24 vet 错误，已改成 `fmt.Errorf("%s", errorString)`。
- 排除上述无限等待测试与两项外部证书测试后，其余 `go test ./...`：全部通过（vet 保持开启）。没有修改旧测试来伪装全绿。
- macOS 默认 CGO 的 core/sing 构建会触发原依赖 `resolv_darwin_cgo.go` 的 `_res` C 选择表达式错误；所以全量回归采用 `CGO_ENABLED=0`。独立 AnyTLS 竞态测试使用 CGO 成功运行，链接器有 LC_DYSYMTAB 警告但退出状态为 0。

- 追加 `go test -race ./node ./core -run 'Test(Monitor|SelectorDeleteUserTraffic)'`：通过。
- Linux amd64、arm64 完整发布标签构建：均通过；输出为静态链接 ELF。二进制 build info 已核对 sing-anytls v0.0.13、sing-box_mod v1.12.0-beta.17.2、全部发布标签和目标架构。版本标签为 `anytls-fix-20260929`。
- 这两项 Linux 构建是在 macOS 交叉编译，尚未在用户 Linux 服务器实际启动，也没有进行生产负载 24–48 小时验证。

## 修改文件与提交

- `core/sing/anytls/inbound.go` 和 LICENSE：隔离的 AnyTLS 入站补丁及来源说明。
- `core/sing/sing.go`、`user.go`：注册补丁并使用修复后的类型。
- `core/sing/hook.go`、`node.go`、`core/interface.go`、`core/selector.go`、`node/task.go`：缓存生命周期、用户变更与节点重载。
- `go.mod`、`go.sum`：仅升级 sing-anytls。
- `common/serverstatus/serverstatus.go`：一行修复 Go 1.24 vet 错误。
- `core/sing/anytls/inbound_test.go`、`core/sing/hook_test.go`、`core/sing/lifecycle_test.go`、`core/selector_test.go`、`node/task_test.go`：回归测试。
- 本说明及 `scripts/observe-memory.sh`：部署和观察方法。

本地分支 `fix/anytls-session-lifecycle`。当前没有 GitHub 登录凭据，推送 dry-run 返回 `could not read Username`；未推送、未创建 PR。交付包包含 Git 补丁与源码。取得仓库登录权限后，可推送本地分支并向该 fork 的 main 创建 PR。

## 单节点部署及回滚

先选择一台节点，保持其用户量、配置和流量尽量可比。管理命令 `/usr/bin/V2bX` 可能是 shell 脚本，不能覆盖它。用运行中进程定位真正核心，并检查目标机器架构：

```sh
uname -m
PID=$(systemctl show V2bX --property MainPID --value)
BIN=$(readlink -f "/proc/$PID/exe")
printf '%s\n' "$BIN"
file "$BIN"
```

确认 BIN 是实际 Go 核心，上传与架构匹配的构建产物后备份并替换（以下为 amd64，上传路径自行调整）：

```sh
BACKUP="$BIN.before-anytls-fix"
cp -p "$BIN" "$BACKUP"
install -m 755 ./V2bX-amd64 "$BIN.new"
systemctl stop V2bX
mv "$BIN.new" "$BIN"
systemctl start V2bX
systemctl --no-pager --full status V2bX
journalctl -u V2bX -n 80 --no-pager
```

用 `go version -m "$BIN"` 核对 sing-anytls v0.0.13 和构建标签。若失败，停止服务、从 BACKUP 恢复二进制并重新启动；原配置和数据库没有变动。部署会断开现有连接。

## 观察 24–48 小时

部署前后各采样，覆盖相似流量高峰和低峰；记录面板同步、用户删除和节点重载的时间：

```sh
sudo bash scripts/observe-memory.sh V2bX 60 | tee anytls-memory.csv
```

脚本输出 PID、RSS、匿名内存、swap、OS 线程数、FD 和 TCP 数。PID 变化表示重启，必须分段比较。需要 root 才能可靠获取 `ss` 的进程归属；Threads 不是 Go goroutine 数。

预期：反复空会话连接/断开或重载后，连接/FD 应回落，低峰期内存应趋于稳定，而非随历史会话数持续增长。不能只凭 RSS 不立即下降认定泄漏，Go 运行时可能保留已空闲的堆页。

需要区分 Go 存活堆与 RSS 时，可在单台观察节点的 systemd drop-in 中临时设置 `Environment=GODEBUG=gctrace=1` 并重启；通过 `journalctl -u V2bX` 比较相近负载下每轮 GC 后的存活堆大小。记录好原环境值，观察后删除此次临时配置。gctrace 不提供精确 goroutine 数；进一步归因需要堆/goroutine profile，本补丁没有暴露公开诊断端口。

## 本 fork 的一键安装与发布

发布目标为 `Laosixok/V2BX-malio`。`install.sh` 与管理菜单的安装、更新入口均从这个仓库的 Releases 下载，不再切换回原作者的二进制。

发布者运行 `VERSION=<发布标签> bash build.sh`（Go 1.24.1 或兼容版本），上传 `build/` 下两个 ZIP、`install.sh` 和 `SHA256SUMS` 到同一个 Release。已有已验证二进制时可运行 `bash scripts/package-release.sh` 仅打包。包中包含管理菜单、服务文件和配置向导，无需另从上游 master 下载脚本。

```bash
curl -fL --retry 3 https://github.com/Laosixok/V2BX-malio/releases/latest/download/install.sh -o /tmp/V2bX-install.sh && bash /tmp/V2bX-install.sh
```

在该仓库正式发布 Release 之前，此命令不可用。只支持 Linux amd64 / arm64；可追加 Release 标签指定版本。脚本先下载并校验所选 ZIP 的 SHA256、完整性和必需文件，成功后才停止服务、替换核心。升级保留 `/etc/V2bX` 已有配置，旧核心备份为 `/usr/local/V2bX/V2bX.previous`。这不是所有系统的完整事务回滚；安装后仍需检查 `V2bX status`、日志及实际连接。

管理脚本继承原上游菜单；首次安装的配置向导仍需要填写服务器实际面板信息。安装脚本已在隔离临时目录通过下载失败、校验失败、缺少文件、正常更新四种模拟测试，没有在真实 Linux 服务器执行安装。测试命令：`python3 scripts/test-installer.py`。
