-- CBI form for the 9router-go UCI settings.
--
-- Same tree as the controller: Lua CBI, native on 18.06/19.07. Fields map
-- one-to-one onto /etc/config/9router-go, which the procd init script reads.
--
-- The status panel is a SimpleSection whose template is the htm file beside
-- this one. That is the 18.06-compatible way to put non-form content on the same
-- page: CBI renders it, the form's Apply still saves, and no second menu entry
-- is needed.

local m, s, o

m = Map("9router-go", translate("9router-go"),
	translate("AI API proxy gateway. Settings take effect after a restart."))

s = m:section(SimpleSection, translate("Status"))
s.template = "9router-go/overview"

s = m:section(NamedSection, "main", "config", translate("General"))
s.addremove = false

o = s:option(Flag, "enabled", translate("Enabled"),
	translate("Start the gateway when the router boots."))
o.default = "1"
o.rmempty = false

o = s:option(Value, "port", translate("Port"),
	translate("The dashboard is served from this port as well."))
o.datatype = "port"
o.placeholder = "20130"
o.rmempty = false

o = s:option(Value, "host", translate("Listen address"),
	translate("Leave empty to listen on every interface. 127.0.0.1 keeps the dashboard off the LAN."))
o.datatype = "host"
o.placeholder = translate("all interfaces")

o = s:option(Value, "data_dir", translate("Data directory"),
	translate("Writable storage for the database, downloaded cloudflared and the runtime binary."))
o.datatype = "string"
o.placeholder = "/opt/9router"
o.rmempty = false

o = s:option(Flag, "auto_update", translate("Self-update"),
	translate("Let the gateway replace its own binary under the data directory. Leave off to update through opkg only."))

o = s:option(Flag, "healthcheck", translate("Health check"),
	translate("Probe /health once a minute and restart the gateway after a run of failures."))
o.default = "1"
o.rmempty = false

o = s:option(Value, "healthcheck_threshold", translate("Health check failures before restart"),
	translate("Consecutive failed probes before the gateway is restarted."))
o.datatype = "uinteger"
o.placeholder = "3"
o:depends("healthcheck", "1")

o = s:option(Value, "healthcheck_timeout", translate("Health check timeout (seconds)"),
	translate("How long a single probe may take before it counts as a failure."))
o.datatype = "uinteger"
o.placeholder = "5"
o:depends("healthcheck", "1")

o = s:option(Flag, "open_lan", translate("Open LAN firewall"),
	translate("Allow the dashboard to be reached from the LAN. Disable this to keep it accessible only from the router itself."))
o.default = "0"

o = s:option(ListValue, "log_level", translate("Log level"))
o:value("debug", "debug")
o:value("info", "info")
o:value("warn", "warn")
o:value("error", "error")
o.default = "info"
o.rmempty = false

return m
