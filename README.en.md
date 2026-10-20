# rime-translate: live Chinese → English for rime-ice

[中文](README.md) | **English**

Type pinyin in **fcitx5-rime + rime-ice (雾凇拼音)**, pause for 2 seconds, and the second candidate becomes the English translation. Works in every application, on both X11 and Wayland.

```
type nihaoshijie   →  1.你好世界  2.你好  3.拟好 …
pause 2 seconds    →  1.你好世界  2.Hello world〔译〕  3.你好 …
press 2            →  commits "Hello world" (Space still commits the Chinese)
```

## How it works

```
fcitx5 ─ Rime (rime-ice)
           ├─ cn2en_filter.lua      on every candidate refresh: take the first candidate → GET /auto
           │                          cache hit  → insert the translation as candidate #2
           │                          cache miss → server returns 204 at once and starts a 2 s debounce timer
           └─ cn2en_processor.lua   on refresh key F18 → recompose (the filter now hits the cache)
                                    on commit / clear  → GET /cancel
                    ▲ virtual keyboard on /dev/uinput presses F18
                    │
                    rime-translate (Go, systemd user service)
                      debounce · cache (memory + disk) · request coalescing · ordered backend fallback
                      google ─▶ OpenAI-compatible (OpenAI/DeepSeek/Ollama…) ─▶ baidu
```

* **Why a virtual keyboard**: Rime and fcitx5 only refresh the candidate list on a key event; there is no timer. When a translation is ready, the service taps F18 on a one-key virtual keyboard (it maps to `XF86Launch9`, which GNOME and normal applications ignore). The plugin swallows that key and refreshes the candidates.
* **Never blocks typing**: on a cache miss the service answers immediately, and curl has a 0.5 s hard timeout. Typing, committing or clearing during the countdown cancels the pending translation.
* **Works without uinput access too**: the translation then shows up on your next key press (e.g. Shift).

## Install

```bash
./install.sh          # installs fcitx5-rime and librime-plugin-lua, fetches rime-ice, builds and starts the service
```

The script:
1. Runs `apt install fcitx5-rime librime-plugin-lua curl git golang-go` (pacman and dnf are detected too)
2. Clones rime-ice into `~/.local/share/fcitx5/rime` if it isn't there yet, backing up any existing directory first
3. Copies the two Lua files and writes `rime_ice.custom.yaml` (an existing file is never overwritten; a `.cn2en` file is written for you to merge by hand)
4. Installs the udev rule `70-rime-translate-uinput.rules` with sudo, so the logged-in user can write `/dev/uinput`
5. Builds `~/.local/bin/rime-translate`, creates `~/.config/rime-translate/config.json`, and registers and starts the systemd user service

Two manual steps remain:
1. Run `fcitx5-configtool`, click "+" and add **Rime (中州韵)**. Optionally drag it above "Pinyin"
2. Choose "Redeploy" (重新部署) in the tray menu, or run `fcitx5 -rd`. The first build of the dictionary takes a few dozen seconds

Requires Go 1.21 or newer. Standard library only, no third-party dependencies.

## Translation backends

Edit `~/.config/rime-translate/config.json`, then run `systemctl --user restart rime-translate`.

**Google (default, no key needed; needs a proxy in mainland China)**
```json
"backends": ["google"],
"proxy": "http://127.0.0.1:7890"
```
Set `proxy` to your Clash HTTP/mixed port (Clash Verge defaults to 7897, classic Clash to 7890). The systemd service does not inherit `HTTPS_PROXY` from your shell, so it has to go in the config.

**LLM (most natural output), any OpenAI-compatible API**
```json
"backends": ["openai", "google"],
"openai": {
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "sk-...",
  "model": "deepseek-chat"
}
```
You can leave `api_key` out and use an environment variable instead: `systemctl --user edit rime-translate` → `Environment=RIME_TRANSLATE_OPENAI_KEY=sk-...`.
Local Ollama: `base_url: http://127.0.0.1:11434/v1`, `model: qwen2.5:7b`, and any non-empty `api_key`.
For a slow model, raise `timeout_ms`; the waiting happens in the service and never blocks the input method.

**Baidu (reachable from mainland China, free quota)**
```json
"backends": ["baidu"],
"baidu": { "appid": "...", "secret": "..." }
```

With several `backends`, they are tried in order; if one fails, the next one is used.

`auto_delay_ms` (default 2000) is how long to wait after the last keystroke. `refresh_key` (default 188 = F18) is the Linux key code the virtual keyboard presses; set it to 0 to disable.

## Plugin options (`rime_ice.custom.yaml` → `cn2en:`, redeploy after changes)

| Key | Default | Description |
|---|---|---|
| `server` | `http://127.0.0.1:8765` | Address of the local service |
| `position` | `2` | Position of the translation candidate. 2 keeps Chinese first; 1 makes Space commit the English |
| `max_time` | `0.5` | Maximum seconds to wait for the local service |
| `refresh_keycode` | `0x1008FF49` | Keysym of the refresh key (F18 → XF86Launch9); must match the service's `refresh_key` |
| `comment` | `〔译〕` | Marker shown after the candidate |
| `to` | (service config) | Target language, e.g. `ja`; can differ per schema |

Double-pinyin users: copy the `patch:` section of `rime_ice.custom.yaml` into the matching `double_pinyin*.custom.yaml`.

## Troubleshooting

```bash
curl 'http://127.0.0.1:8765/t?q=%E4%BD%A0%E5%A5%BD'   # is the service working?
journalctl --user -u rime-translate -f                # service log (backend and latency of each translation)
ls ~/.local/share/fcitx5/rime/build/rime_ice.schema.yaml && grep -n cn2en $_   # was the patch deployed?
```

* No translation after pausing: Rime probably hasn't been redeployed, the active input method isn't Rime, or `librime-plugin-lua` is missing. Also check `journalctl` for backend failures
* Translation only appears after another key press: the service couldn't open `/dev/uinput` and logs `virtual key unavailable`. Check the udev rule, or log out and back in
* Google returns 429: the service already forces HTTP/1.1 (Google rejects Go's HTTP/2 requests). If you still get 429, your IP is being rate-limited; use a proxy or another backend

## Tests

```bash
cd server && go test ./...                              # fallback, cache, coalescing, debounce and cancel
lua tests/test_lua.lua rime/lua http://127.0.0.1:8766   # needs a fake server: /auto?q=X returns "EN:X", q starting with 慢 returns 204
```

## Uninstall

```bash
./uninstall.sh   # removes the service and Lua plugin; keeps rime-ice, your config and the cache
```

## License

[MIT](LICENSE)
