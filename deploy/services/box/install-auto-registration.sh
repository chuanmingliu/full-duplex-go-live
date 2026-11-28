#!/bin/sh
# Run from the validated ThinkPad release directory.
set -eu
cd "$(dirname "$0")"
test "$(id -u)" = 0
sha256sum -c SHA256SUMS
test -d /home/chuanming/golive-crm/registration-queue
if systemctl cat golive-auto-registration.timer >/dev/null 2>&1; then
    systemctl disable --now golive-auto-registration.timer
fi
install -d -m 0755 /usr/local/libexec /etc/golive
install -m 0755 add-outbound-recipient.py /usr/local/libexec/golive-auto-registration.py
/usr/bin/python3 -I /usr/local/libexec/golive-auto-registration.py --sync-scheduled --prepare
install -m 0644 golive-auto-registration.service golive-auto-registration.path /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now golive-auto-registration.path
systemctl is-active golive-auto-registration.path
