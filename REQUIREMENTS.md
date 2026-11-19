# MyGo Music × Spotify 适配需求（独立实现版）

> 本文档用于新会话：从零独立实现一个「完美适配」的 Spotify 插件。
> 现状仓库已与上游 `xingkaixin/mygo-music-spotify` 解除 fork 关系，可自由改写。
>
> 当前实现（2026-11-19）：已按选项 B 改成 `playback: "stream"`，插件为 JS 外壳 +
> 随包分发的 Go 助手。Go 助手复用 `go-librespot` 的登录、音频密钥、CDN 下载和
> AES 解密；播放与下载只输出 MP3/FLAC，Ogg 仅作为内部源，不向 MyGo 输出。
> Spotify Connect 不接入。

## 0. 一句话目标

做一个**像原生一样**的 Spotify 插件：播放走 MyGo Music 自己的播放器，下载走 MyGo Music 自己的下载管理器，不依赖 Spotify 官方 App，也尽量不依赖用户手动装 librespot。

## 1. 需求（用户视角）

1. **播放**：在 MyGo 里点播 Spotify 歌曲，暂停/跳转/上一首下一首/队列/歌词/可视化都要正常，和其它音源一致。
2. **下载**：歌曲菜单的 `Download…`、列表的 `Download Selected`、歌单的 `Download Playlist` 都能用；存下来的是**原始音频，不做二次转码**。
3. **播放与下载互不干扰**：下载不打断播放，播放不因下载衰减。
4. **安装尽量简单**：最好一键装完即可用，不要「先 brew install librespot 再手动编译 fork」。
5. **库浏览**：搜索、专辑、艺人、歌单、喜欢、收藏、播客、电台、评论、歌词——能力对齐上游现有水平或更好。

## 2. 现状（已完成，可复用/可推翻）

| 组件 | 位置 | 状态 |
|---|---|---|
| 插件仓库 | `src/mygo-music-spotify`（`ybbapp/mygo-music-spotify`） | v0.4.0，`playback: "stream"`，已解除 fork |
| librespot fork | `src/librespot`（`ybbapp/librespot`），dev 分支有 `feat/stream-server` PR #2 | 新增 `--stream-port` 本地流服务 |
| 已装 librespot | `/usr/local/bin/librespot` | 带 `--stream-port`（ad-hoc 构建） |
| 宿主 | `src/mygo/mygo-music`（`ybbapp/mygo-music`） | 已放宽 `canDownload`：远程源只要 `can("stream")` 即可下载 |
| 已装 App | `/Applications/MyGo Music.app` | 含上述宿主改动（ad-hoc 签名） |

- 备份：`~/.mygo-backups/mygo-music-spotify-*.bundle`、`~/.mygo-plugin-backups/`
- 工作路径均在 `/Volumes/Home/ybbapp/...`（`$HOME=/Volumes/Home/ybbapp`）

### 现状的不足（"不够完美"之处）

1. `playback: "remote"`：MyGo 只是遥控器，跳转/歌词时间轴/可视化/缓冲都受 Connect 设备制约，且**下载必须另起一条代码路径**。
2. 流服务**整首缓冲**后才回响应（`collect()` 返回 `Vec<u8>` + `Full::new`），首字节延迟高、不支持 Range，不适合当播放源。
3. 依赖外部 librespot 可执行文件，且 fork 版需要用户自己 `cargo build`。
4. 两次登录：插件 PKCE（Web API）＋ librespot OAuth（音频）。

## 3. 硬约束（已核实，务必遵守）

**Spotify 侧**

- Web API **不提供音频**；音频只能经 librespot 一类的客户端解密取得。
- 播放需 **Premium**；Development Mode 上限 5 个用户，且很多接口（非本人歌单等）403。
- 较新账号可能拿不到音频解密密钥（librespot#1649），此时播放与下载都会失败。

**插件运行时（宿主内嵌 JS 引擎）**

- 只有 JS 标准库 + 定时器 + `host` 全局；**没有** `fs`、`crypto.subtle`、`Buffer`、Node API。
- `host` 提供：`platform/arch/dir/dataDir`、`storage`、`language/t`、`setting`、`openURL`、`signInPrompt`、`crypto.{md5,aesEcbEncrypt}`、`waitForRedirect`、`webSignIn`、`spawn`、`removeDataDir`。
- **`host.spawn`**：可执行文件须在 `plugin.json` 的 `permissions.process` 名单里；支持绝对路径、PATH 名字、或**插件目录内的相对路径**（如 `bin/helper-*`）。需监听器在设置里授权。进程随插件结束。
- `host.spawn` **不给子进程传环境变量**；Finder 启动的 App PATH 里没有 Homebrew，故必须用绝对路径或插件内路径。
- 插件调用有 **30 秒超时**（`callTimeout`）；`signIn` 单独放宽到 5 分钟。

**宿主下载/播放契约**

- `Source.Stream(id, quality)` 返回 `music.Stream{url,type,quality,bitRate,size,peak,gain,headers}`。
- 宿主对 URL 发普通 `GET`（无 Range），跟随重定向；支持自定义 headers。
- 支持格式：`mp3/flac/ogg/wav/m4a/aac`。**Ogg Vorbis 可原生解码**（`jfreymuth/oggvorbis`）。
- `canDownload` 现为：`lib != nil && lib.id != local.ID && lib.can("stream") && !tr.Unavailable`（不再要求 `PlaybackStream`）。
- `requestHeaders` 可给音频请求附带 `{setting:key}`/`{storage:key}` 的 header。
- 插件 `fetch` 在获授权后可达 `127.0.0.1`/`localhost`。

**插件安装器**

- 支持按平台挑 release 资产：资产名含 `darwin`+`arm64` 的 `.zip` 优先（`pickAsset`）。
- **解压/拷贝保留可执行位**（0755）；上限 `maxUnpacked = 2GB`、`maxFiles = 50000`。
- 无 release 时回落到默认分支源码 zip。

## 4. 方案选项

### 选项 A：维持 `remote`（当前）

- 优点：改动小，库浏览已完整。
- 缺点：不是「原生播放」，下载要单开路径，依赖外部 librespot。

### 选项 B：`playback: "stream"` + 随插件分发的本地助手（**推荐**）

- 插件改为 `playback: "stream"`，`stream` 返回 `http://127.0.0.1:<port>/stream?uri=…`。
- 随插件（按平台）分发一个基于 librespot 的助手可执行文件，负责：
  - Spotify 登录（OAuth 或缓存凭据）
  - **流式**提供原始音频（分块、支持 Range、边解边发，而非整首缓冲）
  - 返回 Ogg Vorbis，`type: "ogg"`
- 播放与下载**共用同一个 URL**：宿主播放器直接播，下载管理器直接存，零特判。
- 优点：真正的原生播放（精确 seek/歌词/可视化/gapless）；下载天然可用；一次安装即用；不依赖官方 App。
- 代价：需把 librespot 编译成可分发助手（每平台一个），并保证流式输出。

### 选项 C：选项 B + 助手兼作 Connect 设备

- 在 B 基础上让助手同时广播一个 Connect 设备（可选能力）。
- 仅在「仍想用手机 App 遥控本机播放」时才需要，通常可省。

## 5. 推荐落地路径（选项 B）

1. **助手可执行文件**（Rust，基于 librespot 库）
   - 参数：`--port`、`--system-cache`、登录方式（`--enable-oauth` 或缓存凭据）。
   - 端点：`GET /stream?uri=spotify:track:<id>&quality=<standard|high|lossless>`
     - **必须流式**：边解密边写响应体；支持 `Range`（宿主 seek 需要）。
     - 内容：Ogg Vorbis（去 Spotify 私有头包，音频包原样），`Content-Type: audio/ogg`。
   - `GET /health` 供插件探测就绪。
2. **随插件分发**：release 上传 `mygo-music-spotify-<os>-<arch>.zip`，内含 `bin/helper`。安装器按平台自动选。
3. **插件 `plugin.json`**：
   - `"playback": "stream"`
   - `permissions.process: ["bin/helper"]`（插件目录内相对路径）
   - `permissions.network`：`api.spotify.com`、`accounts.spotify.com`、`lrclib.net`
4. **插件 `index.js`**：
   - 保留现有 Web API 全部能力（登录/搜索/歌单/歌词…）。
   - `stream()`：确保证书就绪 → 返回助手 URL。
   - 助手生命周期：首次播放时 `spawn`，退出时随插件结束；端口固定或动态探测。
5. **宿主**：现有改动已够（`canDownload` 放宽）。**无需再改宿主**，除非要修 UI 文案。

## 6. 验收标准

- [ ] 断网/未登录时有清晰提示，不白屏。
- [ ] 播放：暂停、±跳转、上下首、队列、歌词、可视化全部正常。
- [ ] 下载：右键 `Download…`、`Download Selected`、`Download Playlist` 均可用；输出只能是 MP3 或 FLAC。`high` 输出 MP3 320，`lossless` 优先原始 FLAC，FLAC 密钥不可用时回退 MP3 320；不再输出 Ogg。
- [ ] 首字节延迟 < 1s（流式，非整首缓冲）。
- [ ] 拖动进度条可 seek（助手支持 Range）。
- [ ] 卸载 librespot/清空缓存后，重装插件仍可一键可用。
- [ ] 不依赖 Spotify 官方 App 就行。

## 7. 风险与待定

- **密钥限制**：新账号可能拿不到解密密钥（librespot#1649）——需给出明确报错文案。
- **ToS/法律**：绕过 DRM 分发音频有合规风险，属用户自担范围。
- **代理**：librespot 不走系统代理，需 TUN 模式。
- **助手体积**：Rust release 约 15–30MB/平台，在 2GB 上限内没问题。
- **双登录**：可否让助手与插件共用一次登录？（待研究 librespot OAuth 是否可复用 Web API token）
- **播放模式切换成本**：从 `remote` 改 `stream` 会改变交互（不再有设备菜单），需确认用户接受。

## 8. 关键代码位置（供新会话快速定位）

宿主：

- `internal/plugin/host.go:335` `spawn`；`internal/plugin/plugin.go:95` `Programs.Allows`
- `internal/plugin/plugin.go:283` `Stream` 调用；`internal/app/download.go:41` `canDownload`
- `internal/player/stream.go` 拉流播放；`internal/player/download.go` 下载
- `internal/plugin/install.go:271` `pickAsset`（按平台选资产）

librespot fork：

- `src/stream_server.rs` 本地流服务（当前整首缓冲，需改流式）
- `playback/src/download.rs`、`playback/src/player.rs`（解密链路参考）

插件：

- `index.js` 现有 Web API 与 `remote*` 导出、`stream` 导出
