# Spotify for MyGo Music

[English](README.md) | 简体中文

在 [MyGo Music](https://github.com/egoist/mygo-music) 里浏览、播放和下载 Spotify。本插件与 Spotify 无关，也未经其认可。

插件使用 MyGo Music 自己的播放器。随插件分发的小型 Go 助手负责连接 Spotify、解密音频，并在 `127.0.0.1` 上提供 MP3 或 FLAC 流。Ogg 只作为内部源，最终不会作为播放或下载结果输出。不需要 Spotify 官方 App，也不需要用户另外安装 librespot。

**播放需要 Spotify Premium。**

## 设置

1. 用 Premium 账号登录 [Spotify 开发者后台](https://developer.spotify.com/dashboard)，点 **Create app**。名称和描述随便填，添加下面这个 Redirect URI，勾选 **Web API**，同意条款后保存：

   ```
   http://127.0.0.1:43110/callback
   ```

2. 从 [ybbapp/mygo-music-spotify](https://github.com/ybbapp/mygo-music-spotify) 安装对应平台的 release。平台 zip 里已经包含 Go 助手。MyGo Music 询问时，允许插件启动程序。

3. 在开发者后台的 App 页面复制 **Client ID**，粘贴到 MyGo Music 的插件设置里，点 **登录**，然后在浏览器里批准授权。

   如果之前装过旧版插件，请先退出再重新登录一次：新的登录会同时申请 Spotify 的 `streaming` 权限，这样助手可以尽量复用 Web 登录。

4. 播放一首歌。插件会启动 Go 助手。如果 Spotify 要求单独授权音频，按提示打开一次配对页面并批准。之后助手会保存并复用这个音频登录。

只有创建这个 App 的账号，以及被加进 App 的账号能登录。Spotify 的 Development Mode 最多允许 5 个账号。要加人，在后台打开这个 App，进入 **User Management**，填入对方账号的名字和邮箱。

## 与原版 v0.2.0 的区别

| | 原版 v0.2.0 | 当前版本 |
|---|---|---|
| 播放模式 | `remote`：MyGo 遥控 Spotify Connect 设备 | `stream`：MyGo 自己播放 |
| 音频来源 | Spotify 官方 App 或外部 librespot | 插件自带的 Go helper |
| 安装 | 需要安装 librespot；下载还要特殊 Rust 构建 | 平台 zip 自带 helper |
| 下载 | 从特殊 librespot 接口保存原始 Ogg | 只保存 MP3 或 FLAC；Ogg 仅内部使用 |
| 设备菜单 | 可以选择 Spotify Connect 设备 | 没有设备菜单 |
| 手机 Spotify App | 可以看到并遥控 Connect 设备 | 看不到，也不能遥控 MyGo |
| 队列 | 使用私密歌单 “MyGo Music Queue” | 由 MyGo 自己管理队列 |
| 登录 | Web API 登录 + librespot 登录 | Web 登录 + helper 音频登录，尽量复用 token |

## 工作方式

- `index.js` 是 MyGo Music 插件入口，保留 Web API 登录、曲库浏览、歌单、播客和歌词能力。
- `helper/` 是 Go 二进制，复用 `go-librespot` 的 Spotify 登录、音频密钥、CDN 下载和 AES 解密。
- 助手只在回环地址上提供 `GET /stream?uri=spotify:track:...&quality=high`。
- `high` 从 Spotify 320k Ogg 源转成 MP3 320。
- `lossless` 在 FLAC 密钥可用时直接返回原始 FLAC，否则回退 MP3 320。
- `standard` 返回 MP3 160。Ogg 永远不会发给 MyGo Music。
- 插件通过 `host.spawn` 启动助手；MyGo Music 退出时助手结束。

本地流支持普通 HTTP 读取和 byte range。MyGo Music 的播放器和下载管理器使用同一个 URL，所以下载不需要另一套音频路径。

## 下载

歌曲菜单的 **Download…**、列表的 **Download Selected**、歌单的 **Download Playlist** 都读取同一条本地流。下载结果只会是 MP3 或 FLAC。`lossless` 优先原始 FLAC；如果 Spotify 拒绝 FLAC 密钥，自动回退 MP3 320；`high` 和 `standard` 使用 MP3。

## 从源码构建

需要 Go 1.27 或更新版本。助手是纯 Go，不需要 libogg、libvorbis 或 Rust 工具链。

```
./scripts/build-helper.sh
./scripts/package-release.sh
```

`build-helper.sh` 生成 `bin/helper-<os>-<arch>`；`package-release.sh` 在 `dist/` 下生成平台 zip。GitHub release 会为 macOS、Linux 和 Windows 构建这些资产。

## 支持的功能

- 首页：最近播放、常听歌曲、常听艺人、收藏的专辑
- 搜索歌曲、专辑、艺人、歌单和播客
- 专辑、艺人、歌单、喜欢的歌曲、收藏的专辑和播客
- 喜欢歌曲，收藏专辑、歌单和播客
- 新建、重命名、删除歌单，添加、移除、调整歌单里的歌曲顺序
- 通过 MyGo Music 播放、暂停、拖动进度、上一首/下一首、队列、音量和可视化
- 从同一条流下载歌曲和歌单
- 来自 [LRCLIB](https://lrclib.net) 的滚动歌词（有收录时）

这个模式没有 Spotify Connect 设备菜单：声音直接由 MyGo Music 播放。

## Spotify 不允许的功能

Spotify 给 Development Mode 应用开放的 Web API 比它自家 App 少：

- 不是你创建、也不是你参与协作的歌单（包括 Spotify 官方歌单）会出现在侧边栏，但打不开。
- 没有编辑推荐、排行榜、新歌速递、推荐和电台。
- 拿不到艺人的热门歌曲。艺人页显示的是搜索这个艺人名字时排在前面的歌。
- 不能关注艺人。
- 搜索每页只返回 10 条结果。

## 常见问题

**Spotify audio sign-in is required**：打开插件提示里的配对网址，批准授权，然后重新播放。

**Spotify refused this account's audio key**：较新的 Spotify 账号可能被 Spotify 拒绝提供音频密钥（[librespot#1649](https://github.com/librespot-org/librespot/issues/1649)），目前没有已知绕过方法。

**lossless 下仍回退 MP3**：Spotify 可能列出了 FLAC 文件，却拒绝提供它的密钥。助手会自动从 Ogg 320 转成 MP3 320。

**Playing through Spotify needs Premium**：登录的账号是 Spotify Free。

**Spotify is limiting requests: try again in a moment**：Development Mode 有请求频率限制。等一会儿再试。

**需要代理才能访问 Spotify 时，什么都加载不出来**：MyGo Music 和助手直接连接 Spotify，不走系统代理设置。打开代理软件的 TUN 模式（有的叫增强模式）。

## 隐私

你的 Spotify Web API token 以明文保存在本机的插件数据里，只发给 Spotify。助手自己的音频会话保存在同一数据目录下的 `spotify-audio/` 里；退出登录会清除它，删除这个目录也会忘记音频登录。要在 Spotify 那边撤销授权，到 Spotify 账号的 **Manage apps** 页面移除这个 App。

每首歌的标题、艺人、专辑和时长会发给 LRCLIB 来查歌词。
