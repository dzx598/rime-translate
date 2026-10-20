#!/usr/bin/env bash
# 卸载 cn2en 插件与 rime-translate 服务（保留雾凇拼音本身和翻译缓存）
set -euo pipefail
RIME_DIR="${RIME_DIR:-$HOME/.local/share/fcitx5/rime}"

systemctl --user disable --now rime-translate.service 2>/dev/null || true
rm -f "$HOME/.config/systemd/user/rime-translate.service" "$HOME/.local/bin/rime-translate"
systemctl --user daemon-reload || true
if [[ -f /etc/udev/rules.d/70-rime-translate-uinput.rules ]]; then
  sudo rm -f /etc/udev/rules.d/70-rime-translate-uinput.rules && sudo udevadm control --reload || true
fi
rm -f "$RIME_DIR/lua/cn2en_processor.lua" "$RIME_DIR/lua/cn2en_filter.lua" "$RIME_DIR/rime_ice.custom.yaml.cn2en"

echo "已移除服务和 Lua 插件。"
echo "请手动从 $RIME_DIR/rime_ice.custom.yaml 删除 cn2en 相关的三处 patch，然后重新部署 Rime。"
echo "配置 ~/.config/rime-translate 和缓存 ~/.cache/rime-translate 已保留，不需要可自行删除。"
