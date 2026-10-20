#!/usr/bin/env bash
# 一键安装：fcitx5-rime + 雾凇拼音 + cn2en 翻译插件 + rime-translate 本地服务
# 用法：./install.sh            安装 / 更新
#       ./install.sh --no-deps  跳过系统包安装（已装好 fcitx5-rime 时）
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RIME_DIR="${RIME_DIR:-$HOME/.local/share/fcitx5/rime}"
BIN_DIR="$HOME/.local/bin"
CFG_DIR="$HOME/.config/rime-translate"
UNIT_DIR="$HOME/.config/systemd/user"
TS="$(date +%Y%m%d-%H%M%S)"

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- 1. 系统依赖 ----------
if [[ "${1:-}" != "--no-deps" ]]; then
  say "安装系统依赖（需要 sudo）"
  if command -v apt-get >/dev/null; then
    sudo apt-get install -y fcitx5-rime librime-plugin-lua curl git golang-go
  elif command -v pacman >/dev/null; then
    sudo pacman -S --needed --noconfirm fcitx5-rime curl git go   # Arch 的 librime 已内置 lua 插件
  elif command -v dnf >/dev/null; then
    sudo dnf install -y fcitx5-rime librime-lua curl git golang
  else
    warn "未识别的包管理器，请自行安装 fcitx5-rime、librime-lua、curl、git、go"
  fi
fi
command -v curl >/dev/null || die "缺少 curl"
command -v git  >/dev/null || die "缺少 git"
command -v go   >/dev/null || die "缺少 Go 工具链（https://go.dev/dl/），装好后重新运行"

# ---------- 2. 雾凇拼音 ----------
if [[ -f "$RIME_DIR/rime_ice.schema.yaml" ]]; then
  say "已检测到雾凇拼音：$RIME_DIR（跳过下载）"
else
  if [[ -d "$RIME_DIR" && -n "$(ls -A "$RIME_DIR" 2>/dev/null)" ]]; then
    say "备份现有 Rime 配置 -> $RIME_DIR.bak.$TS"
    mv "$RIME_DIR" "$RIME_DIR.bak.$TS"
  fi
  say "下载雾凇拼音 rime-ice"
  mkdir -p "$(dirname "$RIME_DIR")"
  git clone --depth 1 https://github.com/iDvel/rime-ice.git "$RIME_DIR"
fi

# ---------- 3. Lua 插件 + 方案补丁 ----------
say "安装 cn2en Lua 插件"
mkdir -p "$RIME_DIR/lua"
install -m 644 "$HERE/rime/lua/cn2en_processor.lua" "$RIME_DIR/lua/"
install -m 644 "$HERE/rime/lua/cn2en_filter.lua"    "$RIME_DIR/lua/"

CUSTOM="$RIME_DIR/rime_ice.custom.yaml"
if [[ ! -f "$CUSTOM" ]]; then
  install -m 644 "$HERE/rime/rime_ice.custom.yaml" "$CUSTOM"
  say "已写入 $CUSTOM"
elif grep -q 'hotkey: "Control+t"' "$CUSTOM"; then
  # 旧版（Ctrl+T 触发）生成的补丁：备份后换成自动翻译版
  mv "$CUSTOM" "$CUSTOM.bak.$TS"
  install -m 644 "$HERE/rime/rime_ice.custom.yaml" "$CUSTOM"
  say "已把旧版 Ctrl+T 补丁升级为自动翻译（旧文件备份为 $CUSTOM.bak.$TS）"
elif grep -q cn2en "$CUSTOM"; then
  say "$CUSTOM 已包含 cn2en 配置（未改动）"
else
  install -m 644 "$HERE/rime/rime_ice.custom.yaml" "$CUSTOM.cn2en"
  warn "你已有 $CUSTOM，未覆盖。"
  warn "请把 $CUSTOM.cn2en 里 patch: 下面的内容合并进去，然后重新部署。"
fi

# ---------- 4. 翻译服务 ----------
say "编译 rime-translate"
mkdir -p "$BIN_DIR"
( cd "$HERE/server" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BIN_DIR/rime-translate" . )

UDEV_RULE=/etc/udev/rules.d/70-rime-translate-uinput.rules
if [[ ! -w /dev/uinput ]]; then
  say "授权当前用户写 /dev/uinput（自动翻译完成后用它刷新候选，需要 sudo）"
  if sudo install -m 644 "$HERE/server/70-rime-translate-uinput.rules" "$UDEV_RULE" &&
     sudo udevadm control --reload && sudo udevadm trigger --action=change --name-match=uinput; then
    sleep 1
  fi
  [[ -w /dev/uinput ]] || warn "仍然没有 /dev/uinput 写权限：译文要等下一次按键才会出现（注销重新登录后再试）"
fi

mkdir -p "$CFG_DIR"
if [[ ! -f "$CFG_DIR/config.json" ]]; then
  install -m 600 "$HERE/server/config.example.json" "$CFG_DIR/config.json"
  say "已生成配置 $CFG_DIR/config.json（默认 google 后端）"
else
  say "保留现有配置 $CFG_DIR/config.json"
fi

say "注册 systemd 用户服务"
mkdir -p "$UNIT_DIR"
install -m 644 "$HERE/server/rime-translate.service" "$UNIT_DIR/rime-translate.service"
systemctl --user daemon-reload
systemctl --user enable --now rime-translate.service
systemctl --user restart rime-translate.service
sleep 1
if curl -s --max-time 2 http://127.0.0.1:8765/health | grep -q ok; then
  say "服务已启动：http://127.0.0.1:8765"
  if out=$(curl -s -f --max-time 10 'http://127.0.0.1:8765/t?q=%E4%BD%A0%E5%A5%BD%E4%B8%96%E7%95%8C'); then
    say "试翻译「你好世界」-> $out"
  else
    warn "试翻译失败：看日志 journalctl --user -u rime-translate -n 30"
    warn "google 后端在国内需要在 config.json 里填 \"proxy\": \"http://127.0.0.1:<Clash 端口>\""
  fi
else
  warn "服务没起来：journalctl --user -u rime-translate -n 30"
fi

# ---------- 5. 后续手动步骤 ----------
cat <<EOF

$(say "完成！还差两步：")
  1) 把 Rime 加进 fcitx5：运行 fcitx5-configtool → 左下「+」→ 搜索「Rime / 中州韵」→ 添加，
     （可选）把它拖到「拼音」上面作为默认输入法。
  2) 重新部署 Rime：托盘图标右键 → 重新部署；或执行  fcitx5 -rd  重启 fcitx5。
     首次部署雾凇拼音要编译词库，可能需要几十秒。

用法：打拼音后停 2 秒 → 第二个候选出现英文译文（〔译〕），按 2 上屏；空格仍上屏中文。

配置：$CFG_DIR/config.json   （改完 systemctl --user restart rime-translate）
插件：$CUSTOM 中 cn2en: 段     （改完重新部署）
EOF
