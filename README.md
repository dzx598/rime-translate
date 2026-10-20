# rime-translate：雾凇拼音实时中译英

在 **fcitx5-rime + 雾凇拼音** 里打拼音，停手 2 秒，第二个候选自动变成英文译文。适用于所有程序，X11 和 Wayland 都能用。

```
输入 nihaoshijie   →  1.你好世界  2.你好  3.拟好 …
停手 2 秒          →  1.你好世界  2.Hello world〔译〕  3.你好 …
按 2               →  上屏 "Hello world"（按空格仍上屏中文）
```

## 架构

```
fcitx5 ─ Rime(雾凇拼音)
           ├─ cn2en_filter.lua      每次候选刷新：取首选 → GET /auto
           │                          命中缓存 → 插入译文候选（第 2 位）
           │                          未命中 → 服务立即返回 204 并开始 2 秒防抖倒计时
           └─ cn2en_processor.lua   收到刷新键 F18 → 重新组词（filter 再查一次就命中）
                                    输入上屏/清空 → GET /cancel
                    ▲ 虚拟键盘 /dev/uinput 按 F18
                    │
                    rime-translate (Go, systemd 用户服务)
                      防抖 · 缓存(内存+磁盘) · 并发合并 · 后端按顺序回退
                      google ─▶ openai 兼容(OpenAI/DeepSeek/Ollama…) ─▶ baidu
```

* **为什么要虚拟键盘**：Rime/fcitx5 只在收到按键时刷新候选框，没有定时器。服务翻译完成后，用一个只有 F18 一个键的虚拟键盘按一下（映射为 `XF86Launch9`，GNOME 和普通应用都不响应），插件把这个键吃掉并刷新候选。
* **不会卡打字**：未命中时服务立即返回，curl 有 0.5 秒硬超时；倒计时内继续打字、上屏或清空都会作废前一次。
* **没有 uinput 权限也能用**：只是译文要等下一次按键（如 Shift）才显示。

## 安装

```bash
./install.sh          # 安装 fcitx5-rime、librime-plugin-lua，拉取雾凇拼音，编译并启动服务
```

脚本会做这些事：
1. `apt install fcitx5-rime librime-plugin-lua curl git`（pacman/dnf 也能识别）
2. 如果 `~/.local/share/fcitx5/rime` 里还没有雾凇拼音，先备份原目录，再 `git clone` rime-ice
3. 复制两个 Lua 文件，写入 `rime_ice.custom.yaml`（已有同名文件就不覆盖，改为生成 `.cn2en` 供手动合并）
4. 用 sudo 安装 udev 规则 `70-rime-translate-uinput.rules`，让当前登录用户可以写 `/dev/uinput`
5. 编译 `~/.local/bin/rime-translate`，生成 `~/.config/rime-translate/config.json`，注册并启动 systemd 用户服务

装完还需要两步手动操作：
1. 运行 `fcitx5-configtool`，点「+」添加 **Rime（中州韵）**，需要的话把它拖到「拼音」上面
2. 托盘菜单选「重新部署」，或执行 `fcitx5 -rd`。第一次编译词库要几十秒

需要 Go 1.21 或更高版本，只用到标准库，没有第三方依赖。

## 配置翻译后端

编辑 `~/.config/rime-translate/config.json`，改完执行 `systemctl --user restart rime-translate`。

**Google（默认，不需要 key，国内要走代理）**
```json
"backends": ["google"],
"proxy": "http://127.0.0.1:7890"
```
`proxy` 填你 Clash 的 HTTP/mixed 端口（Clash Verge 默认 7897，经典 Clash 是 7890）。systemd 服务不会继承终端里的 `HTTPS_PROXY`，所以要写在配置里。

**大模型（译文最自然），任意 OpenAI 兼容接口**
```json
"backends": ["openai", "google"],
"openai": {
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "sk-...",
  "model": "deepseek-chat"
}
```
也可以不写 `api_key`，改用环境变量：`systemctl --user edit rime-translate` → `Environment=RIME_TRANSLATE_OPENAI_KEY=sk-...`。
本地 Ollama：`base_url: http://127.0.0.1:11434/v1`，`model: qwen2.5:7b`，`api_key` 随便填一个非空值。
用大模型时建议在 `cn2en` 里把 `max_time` 调到 3。

**百度（国内直连，免费额度）**
```json
"backends": ["baidu"],
"baidu": { "appid": "...", "secret": "..." }
```

`backends` 里写多个时按顺序尝试，前一个失败就换下一个。

`auto_delay_ms`（默认 2000）是停止输入多久后翻译；`refresh_key`（默认 188 = F18）是虚拟键盘按的 Linux 键码，设成 0 则不按。

## 插件选项（`rime_ice.custom.yaml` → `cn2en:`，改完重新部署）

| 键 | 默认 | 说明 |
|---|---|---|
| `server` | `http://127.0.0.1:8765` | 本地服务地址 |
| `position` | `2` | 译文候选放在第几位。2 表示首选仍是中文；设成 1 则空格直接上屏英文 |
| `max_time` | `0.5` | 询问本地服务的最长等待秒数 |
| `refresh_keycode` | `0x1008FF49` | 刷新键的 keysym（F18 → XF86Launch9），需与服务端 `refresh_key` 对应 |
| `comment` | `〔译〕` | 候选后面的标记 |
| `to` | （服务端配置） | 目标语言，例如 `ja`；也可以给另一个方案设置不同的语言 |

双拼用户：把 `rime_ice.custom.yaml` 里的 `patch:` 内容复制到对应的 `double_pinyin*.custom.yaml`。

## 排错

```bash
curl 'http://127.0.0.1:8765/t?q=%E4%BD%A0%E5%A5%BD'   # 服务是否正常
journalctl --user -u rime-translate -f                # 服务日志（每次翻译的后端和耗时）
ls ~/.local/share/fcitx5/rime/build/rime_ice.schema.yaml && grep -n cn2en $_   # 补丁是否部署成功
```

* 停手后一直不出译文：多半是还没重新部署，或者当前输入法不是 Rime，或者没装 `librime-plugin-lua`；再看 `journalctl` 里后端是否失败
* 要再按一个键才出译文：服务没拿到 `/dev/uinput`，日志里会有 `virtual key unavailable`；检查 udev 规则，或注销后重新登录
* Google 返回 429：服务已强制使用 HTTP/1.1（Google 会拒绝 Go 的 HTTP/2 请求）；如果仍然 429，说明出口 IP 被限流，换代理或换后端

## 测试

```bash
cd server && go test ./...                              # 回退、缓存、并发合并、防抖与取消
lua tests/test_lua.lua rime/lua http://127.0.0.1:8766   # 需要先起一个假服务：/auto?q=X 返回 "EN:X"，q 以「慢」开头返回 204
```

## 卸载

```bash
./uninstall.sh   # 移除服务和 Lua 插件，保留雾凇拼音、配置和缓存
```
