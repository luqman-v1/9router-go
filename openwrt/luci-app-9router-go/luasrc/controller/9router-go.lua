-- LuCI controller for 9router-go.
--
-- Written for the Lua LuCI that OpenWrt 18.06 and 19.07 run natively (also
-- available as `luci-compat` on 21.02+). This is the tree that works on the
-- oldest release the package supports, so it is the tree the package ships.
--
-- Lifecycle actions go through /etc/init.d/9router-go rather than the gateway's
-- HTTP API so they still work when the gateway is wedged: the init script talks
-- to procd, which does not depend on the process it is asked about.

module("luci.controller.9router-go", package.seeall)

local fs = require "nixio.fs"

function index()
	if not fs.access("/etc/config/9router-go") then
		return
	end

	local page
	page = entry({"admin", "services", "9router-go"}, cbi("9router-go/main"), _("9router-go"), 60)
	page.dependent = false
	page.acl_depends = { "luci-app-9router-go" }

	-- Leaf endpoint the page's buttons post to. Kept separate from the page so
	-- the form's own Apply behaviour is untouched.
	page = entry({"admin", "services", "9router-go", "action"}, call("service_action"))
	page.leaf = true
	page.acl_depends = { "luci-app-9router-go" }
end

-- Every action name is checked against a fixed table before it reaches the
-- shell, so nothing from the request is interpolated into a command.
local ACTIONS = { start = true, stop = true, restart = true }

local function read_state()
	local pid = nil
	local pidtxt = fs.readfile("/var/run/9router-go.pid")
	if pidtxt and #pidtxt > 0 then
		pid = pidtxt:match("^%s*(%d+)")
	end

	local running = false
	if pid and pid ~= "" then
		local ok = os.execute("kill -0 " .. pid .. " 2>/dev/null")
		running = (ok == 0 or ok == true)
	end
	return running, pid
end

function service_action()
	local action = luci.http.formvalue("action")

	if not ACTIONS[action] then
		luci.http.status(400, "bad action")
		luci.http.prepare_content("application/json")
		luci.http.write('{"ok":false,"error":"bad action"}')
		return
	end

	os.execute("/etc/init.d/9router-go " .. action .. " >/dev/null 2>&1")

	-- The init script returns before procd has finished spawning, so the pid
	-- file may still be from the previous process. Reporting the state read a
	-- moment later is honest: the page re-reads it on the next render anyway.
	local running, pid = read_state()

	local body
	if pid then
		body = string.format('{"ok":true,"action":"%s","running":%s,"pid":"%s"}',
			action, tostring(running), pid)
	else
		body = string.format('{"ok":true,"action":"%s","running":%s,"pid":null}',
			action, tostring(running))
	end

	luci.http.prepare_content("application/json")
	luci.http.write(body)
end
