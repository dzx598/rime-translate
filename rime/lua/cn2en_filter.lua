-- cn2en_filter.lua
-- 每次候选刷新时，取首选候选（通常是整句）问本地 rime-translate 服务要译文：
--   - 服务已有缓存：把译文作为一个候选插入（默认第 2 位）。
--   - 还没有：服务开始防抖倒计时（停止输入 2 秒后翻译），翻译好后用虚拟键盘按一下刷新键，
--     cn2en_processor 收到后重新组词，这里再次查询就能拿到译文。
--
-- 只调用 127.0.0.1 的本地服务，未命中时服务立即返回 204，curl 还有硬超时，不会卡打字。

local PENDING = "cn2en_pending"

local function urlencode(s)
  return (s:gsub("[^%w%-%._~]", function(c)
    return string.format("%%%02X", c:byte())
  end))
end

local function has_cjk(s)
  -- UTF-8 中 U+4E00..U+9FFF 的首字节为 0xE4..0xE9
  return s:find("[\228-\233]") ~= nil
end

local M = {}

function M.init(env)
  local cfg = env.engine.schema.config
  local server = cfg:get_string("cn2en/server") or ""
  if not server:match("^https?://[%w%.%-:%[%]]+$") then
    server = "http://127.0.0.1:8765"
  end
  env.server = server
  env.max_time = tonumber(cfg:get_string("cn2en/max_time") or "") or 0.5
  env.position = tonumber(cfg:get_string("cn2en/position") or "") or 2
  if env.position < 1 then env.position = 1 end
  env.comment = cfg:get_string("cn2en/comment") or "〔译〕"
  env.to = cfg:get_string("cn2en/to") or ""
  if not env.to:match("^[%a%-]*$") then env.to = "" end
  env.cache, env.cache_n = {}, 0
end

-- 返回译文；未命中返回 nil（服务端已开始倒计时）
local function lookup(env, text)
  local hit = env.cache[text]
  if hit then return hit end
  local q = env.server .. "/auto?q=" .. urlencode(text)
  if env.to ~= "" then q = q .. "&to=" .. env.to end
  -- urlencode 之后 q 里不会出现引号或空白，可安全放进单引号
  local cmd = string.format("curl -s -f --max-time %s '%s' 2>/dev/null", tostring(env.max_time), q)
  local p = io.popen(cmd)
  if not p then return nil end
  local out = p:read("*a") or ""
  p:close()
  out = out:gsub("^%s+", ""):gsub("%s+$", "")
  if out == "" then return nil end
  if env.cache_n >= 200 then env.cache, env.cache_n = {}, 0 end
  env.cache[text] = out
  env.cache_n = env.cache_n + 1
  return out
end

function M.func(input, env)
  local ctx = env.engine.context
  local tr, first_seen, emitted = nil, false, 0
  for cand in input:iter() do
    if not first_seen then
      first_seen = true
      if has_cjk(cand.text) then
        local en = lookup(env, cand.text)
        if en then
          tr = Candidate("cn2en", cand.start, cand._end, en, env.comment)
          tr.quality = cand.quality
        else
          ctx:set_property(PENDING, "1")
        end
      end
    end
    if tr and emitted + 1 == env.position then
      yield(tr); tr = nil; emitted = emitted + 1
    end
    yield(cand); emitted = emitted + 1
  end
  if tr then yield(tr) end
end

-- 供单元测试使用
M._urlencode = urlencode
M._has_cjk = has_cjk

return M
