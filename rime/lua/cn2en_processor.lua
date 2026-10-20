-- cn2en_processor.lua
-- 自动翻译的配套处理器，做两件事：
--   1. 拦截 rime-translate 服务用虚拟键盘按下的刷新键（默认 F18 = XF86Launch9），
--      重新组词，让 cn2en_filter 取到刚翻译好的结果。这个键永远不会传给应用程序。
--   2. 输入上屏或清空时通知服务取消倒计时，避免对已经不存在的输入按刷新键。
--
-- 与 cn2en_filter 通过 context 属性 "cn2en_pending" 通信：
--   filter 向服务提交了一次倒计时就置为 "1"，这里在输入结束时清空并发送 /cancel。

local kRejected, kAccepted, kNoop = 0, 1, 2
local PENDING = "cn2en_pending"

local function refresh(ctx)
  local ok = pcall(function() ctx:refresh_non_confirmed_composition() end)
  if not ok then
    local i = ctx.input
    ctx.input = i -- 旧版 librime-lua：重设 input 触发重新组词
  end
end

local function server_of(cfg)
  local s = cfg:get_string("cn2en/server") or ""
  if not s:match("^https?://[%w%.%-:%[%]]+$") then s = "http://127.0.0.1:8765" end
  return s
end

local M = {}

function M.init(env)
  local cfg = env.engine.schema.config
  env.keycode = tonumber(cfg:get_string("cn2en/refresh_keycode") or "") or 0x1008FF49
  -- server_of 保证地址里没有引号或空白，可安全放进单引号
  env.cancel_cmd = string.format("curl -s -m 1 '%s/cancel' >/dev/null 2>&1 &", server_of(cfg))
  local ctx = env.engine.context
  env.update_conn = ctx.update_notifier:connect(function(c)
    if not c:is_composing() and c:get_property(PENDING) ~= "" then
      c:set_property(PENDING, "")
      os.execute(env.cancel_cmd)
    end
  end)
end

function M.fini(env)
  if env.update_conn then env.update_conn:disconnect() end
end

function M.func(key, env)
  if key.keycode ~= env.keycode then return kNoop end
  local ctx = env.engine.context
  if not key:release() and ctx:is_composing() then refresh(ctx) end
  return kAccepted
end

M._server_of = server_of

return M
