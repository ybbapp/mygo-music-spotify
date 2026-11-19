# Spotify for MyGo Music

English | [简体中文](README.zh-CN.md)

Browse, play and download Spotify from [MyGo Music](https://github.com/egoist/mygo-music). It isn't affiliated with or endorsed by Spotify.

The plugin uses MyGo Music's own player. A small Go helper bundled with the plugin talks to Spotify, decrypts the audio, and serves MP3 or FLAC on `127.0.0.1`. Ogg is an internal source only and is never offered as a playback or download result. Spotify's app and a separately installed librespot are not needed.

**Playing needs Spotify Premium.**

## Set up

1. Sign in to the [Spotify developer dashboard](https://developer.spotify.com/dashboard) with a Premium account and click **Create app**. Give it any name and description, add this redirect URI, tick **Web API**, agree to the terms and save:

   ```
   http://127.0.0.1:43110/callback
   ```

2. Install a release from [ybbapp/mygo-music-spotify](https://github.com/ybbapp/mygo-music-spotify). The platform zip contains the Go helper for that platform. When MyGo Music asks, allow the plugin to start programs.

3. Copy the **Client ID** from the app's page in the dashboard, paste it in the plugin's settings, then click **Sign In** and approve access in your browser.

   If you used an older version of this plugin, sign out and in once: the new login also asks for Spotify's `streaming` scope, which lets the helper reuse the Web login when possible.

4. Play something. The plugin starts its Go helper. If Spotify asks for audio access separately, approve the pairing page once. The helper then keeps that login and reuses it.

Only the account that made the app, and the accounts added to it, can sign in. Spotify's Development Mode lets in five at most. To add one, open the app in the dashboard, go to **User Management**, and enter the account's name and email.

## How it works

- `index.js` is the MyGo Music plugin entry. It keeps the Web API session, library browsing, playlists, podcasts and lyrics.
- `helper/` is a Go binary. It reuses `go-librespot` for Spotify login, audio keys, CDN reads and AES decryption.
- The helper serves `GET /stream?uri=spotify:track:...&quality=high` on loopback only.
- `high` serves MP3 320, transcoded from Spotify's 320 kbps Ogg source.
- `lossless` serves Spotify's original FLAC when its key is available, otherwise MP3 320.
- `standard` serves MP3 160. Ogg is never sent to MyGo Music.
- The plugin starts the helper with `host.spawn`. The helper ends with the plugin.

The stream supports normal HTTP reads and byte ranges. MyGo Music's player and download manager use the same URL, so downloads do not need a second audio path.

## Downloading

A song's **Download…**, a list's **Download Selected** and a playlist's **Download Playlist** use the same local stream as playback. Downloads are always MP3 or FLAC. `lossless` prefers original FLAC and falls back to MP3 320 if Spotify refuses the FLAC key; `high` and `standard` use MP3.

## Build from source

Building needs Go 1.27 or newer. The helper is pure Go; it does not need libogg, libvorbis or a Rust toolchain.

```
./scripts/build-helper.sh
./scripts/package-release.sh
```

`build-helper.sh` writes `bin/helper-<os>-<arch>`. `package-release.sh` writes a platform zip under `dist/`. GitHub releases build these for macOS, Linux and Windows.

## What works

- Home, made of recently played songs, top tracks, top artists and saved albums
- Search for songs, albums, artists, playlists and podcasts
- Albums, artists, playlists, liked songs, saved albums and podcasts
- Liking songs, saving albums, playlists and podcasts
- Creating, renaming and deleting playlists, and adding, removing and reordering their songs
- Play, pause, seek, previous/next, queue, volume and visualizers through MyGo Music
- Downloading songs and playlists from the same stream
- Timed lyrics from [LRCLIB](https://lrclib.net), when it has them

There is no Spotify Connect device menu in this mode: playback happens in MyGo Music itself.

## What Spotify doesn't let it do

Spotify's Web API gives apps in Development Mode less than its own apps have:

- Playlists you don't own or collaborate on, Spotify's own included, show in the sidebar but can't be opened.
- There are no editorial shelves, charts, new releases, recommendations or radio.
- An artist's top tracks aren't available. The artist page shows what a search for their name ranks first.
- Following artists isn't available.
- Search returns 10 results a page.

## When something goes wrong

**Spotify audio sign-in is required**: open the pairing URL shown in the plugin prompt, approve access, then play again.

**Spotify refused this account's audio key**: newer Spotify accounts can be blocked from librespot's audio-key flow ([librespot#1649](https://github.com/librespot-org/librespot/issues/1649)). No workaround is known.

**FLAC falls back to MP3 even on `lossless`**: Spotify can list a FLAC file but still refuse its key. The helper automatically retries from Ogg 320 as MP3 320.

**Playing through Spotify needs Premium**: the account you signed in with is on Spotify Free.

**Spotify is limiting requests: try again in a moment**: Spotify limits how often a Development Mode app asks. Wait, then try again.

**Nothing loads, where Spotify needs a proxy**: MyGo Music and its helper connect to Spotify directly. Turn on your proxy's TUN mode, sometimes called enhanced mode.

## Privacy

Your Spotify Web API tokens are kept unencrypted in the plugin's data on this machine and are sent only to Spotify. The helper stores its own audio session under `spotify-audio/` in that same data directory. Signing out clears it; deleting `spotify-audio/` also forgets it. To revoke access on Spotify's side, remove the app on your Spotify account's **Manage apps** page.

Each song's title, artist, album and length go to LRCLIB to find its lyrics.
