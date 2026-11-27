# Paths sysupgrade keeps in its backup tarball ("keep settings").
#
# Without this file the package's state is lost on every firmware flash.
# OpenWrt's default keep set is /lib/upgrade/keep.d/base-files-essential, which
# lists only a handful of /etc files, and /etc/sysupgrade.conf, which ships
# commented out. Neither mentions anything this package owns, so before this
# file existed /etc/9router-go and the database were discarded by sysupgrade
# even when the operator chose to keep settings.
#
# What is listed, and what is deliberately not:
#
#   /etc/9router-go          the runtime-version marker and the backups written
#                            by `9router-go-backup`. Small.
#   /opt/9router/db          the SQLite database: every provider connection,
#                            combo and API key. The reason this file exists.
#   /opt/9router/uploads     operator-uploaded media referenced from the
#                            database; useless without it.
#
# /opt/9router/bin is NOT listed. It holds a ~52 MB copy of the binary plus the
# downloaded cloudflared, and the sysupgrade backup is staged in /tmp, which is
# RAM. Tarring 52 MB of RAM on a 128 MB router to preserve a file postinst
# reinstalls anyway would turn a firmware upgrade into an out-of-memory event.
# postinst rewrites that copy from the package on the next boot.
#
# A non-default `data_dir` is covered by hand: add it to /etc/sysupgrade.conf.
# postinst warns when it sees one and the warning is not there.
/etc/9router-go
/opt/9router/db
/opt/9router/uploads
