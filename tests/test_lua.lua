-- 用模拟的 librime-lua 环境测试 cn2en_processor / cn2en_filter
-- 运行：lua tests/test_lua.lua <rime/lua 目录> <fake server 地址，如 http://127.0.0.1:8766>
-- 假服务：/auto?q=X 返回 "EN:X"；q 以「慢」开头时返回 204（模拟未命中）
package.path = arg[1] .. "/?.lua;" .. package.path
local URL = arg[2]

local yielded
function yield(c) yielded[#yielded + 1] = c end
function Candidate(t, s, e, text, comment) return {type=t, start=s, _end=e, text=text, comment=comment, quality=0} end

local function make_env(conf)
  local props = {}
  local update_cbs = {}
  local ctx = { input = "", composing = true, refreshed = 0 }
  function ctx:is_composing() return self.composing end
  function ctx:get_property(k) return props[k] or "" end
  function ctx:set_property(k, v) props[k] = v end
  function ctx:refresh_non_confirmed_composition() self.refreshed = self.refreshed + 1 end
  ctx.update_notifier = { connect = function(_, f) update_cbs[#update_cbs+1] = f; return { disconnect = function() end } end }
  ctx._update = function() for _, f in ipairs(update_cbs) do f(ctx) end end
  local config = { get_string = function(_, k) return conf[k] end }
  return { engine = { context = ctx, schema = { config = config } } }, ctx
end

local REFRESH = 0x1008FF49
local function key(code, release) return { keycode = code, release = function() return release or false end } end
local function input_of(list)
  return { iter = function() local i = 0; return function() i = i + 1; return list[i] end end }
end
local function cands(...) local t = {} for i, s in ipairs({...}) do t[i] = {text=s, start=0, _end=6, quality=1, comment=""} end return t end

local fails = 0
local function check(name, cond) print((cond and "PASS " or "FAIL ") .. name); if not cond then fails = fails + 1 end end

local P = require("cn2en_processor")
local F = require("cn2en_filter")

-- urlencode / cjk / server 校验
check("urlencode quotes", F._urlencode("a'b c;\"") == "a%27b%20c%3B%22")
check("urlencode cjk", F._urlencode("你") == "%E4%BD%A0")
check("has_cjk", F._has_cjk("你好") and not F._has_cjk("hello"))

local env, ctx = make_env({ ["cn2en/server"] = URL })
P.init(env); F.init(env)

-- 处理器：只拦截刷新键
check("other key noop", P.func(key(0x61), env) == 2)
ctx.input = "nihao"
check("refresh key accepted", P.func(key(REFRESH), env) == 1)
check("refresh recomposes", ctx.refreshed == 1)
check("refresh release accepted", P.func(key(REFRESH, true), env) == 1 and ctx.refreshed == 1)
ctx.composing = false
check("refresh swallowed when idle", P.func(key(REFRESH), env) == 1 and ctx.refreshed == 1)
ctx.composing = true

-- 过滤器：命中 -> 默认第 2 位
yielded = {}; F.func(input_of(cands("你好世界", "你好")), env)
check("translation second", #yielded == 3 and yielded[1].text == "你好世界"
  and yielded[2].type == "cn2en" and yielded[2].text == "EN:你好世界" and yielded[3].text == "你好")
-- 只有一个候选时追加在后面
yielded = {}; F.func(input_of(cands("啊")), env)
check("single cand appended", #yielded == 2 and yielded[2].type == "cn2en")

-- 未命中：不插候选，标记 pending；输入结束时清除
yielded = {}; F.func(input_of(cands("慢慢来", "慢")), env)
check("miss -> passthrough", #yielded == 2 and yielded[1].text == "慢慢来")
check("miss -> pending", ctx:get_property("cn2en_pending") == "1")
ctx._update()
check("still composing keeps pending", ctx:get_property("cn2en_pending") == "1")
ctx.composing = false; ctx._update()
check("commit clears pending", ctx:get_property("cn2en_pending") == "")
ctx.composing = true

-- position = 1
local env1 = make_env({ ["cn2en/server"] = URL, ["cn2en/position"] = "1" })
F.init(env1)
yielded = {}; F.func(input_of(cands("你好", "拟好")), env1)
check("position 1", yielded[1].type == "cn2en" and yielded[2].text == "你好")

-- 首选非中文：不翻译
yielded = {}; F.func(input_of(cands("hello", "Hello")), env)
check("skip non-cjk", #yielded == 2)

-- 带引号等特殊字符的中文，不应出 shell 问题
yielded = {}; F.func(input_of(cands("他说'你好'; rm -rf ~ \"测\"")), env)
check("special chars escaped", yielded[2] and yielded[2].text == "EN:他说'你好'; rm -rf ~ \"测\"")

-- 服务不可达：候选原样，不报错
local env4 = make_env({ ["cn2en/server"] = "http://127.0.0.1:1", ["cn2en/max_time"] = "0.5" })
F.init(env4)
yielded = {}; F.func(input_of(cands("你好")), env4)
check("unreachable -> passthrough", #yielded == 1 and yielded[1].text == "你好")

-- 非法地址配置回退默认
local env5 = make_env({ ["cn2en/server"] = "http://x/'; touch /tmp/pwn; '" })
F.init(env5)
check("bad server rejected", env5.server == "http://127.0.0.1:8765")
check("bad server rejected (processor)", P._server_of(env5.engine.schema.config) == "http://127.0.0.1:8765")

print(fails == 0 and "ALL PASS" or (fails .. " FAILED"))
os.exit(fails == 0 and 0 or 1)
