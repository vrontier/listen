#!/bin/bash
# Installs the Workbench on the intranet host. Runs there as root, from the
# directory deploy.sh copied it to, next to the built programs:
#
#   sudo bash install.sh
#
# Idempotent: creates what is missing, replaces programs, units and the
# nginx site, keeps the catalogue and /etc/low/workbench.env.
set -euo pipefail
cd "$(dirname "$0")"

HOST=low.home.arpa
SSL=/etc/ssl/home-arpa

say() { echo "== $*"; }

# User and group.
if ! getent passwd low >/dev/null; then
	say "creating user low"
	useradd --system --home-dir /var/lib/low --no-create-home --shell /usr/sbin/nologin --user-group low
fi

# Programs.
install -m 0755 -o root -g root low-workbench /usr/local/bin/low-workbench.new
install -m 0755 -o root -g root low-standin /usr/local/bin/low-standin.new
mv -f /usr/local/bin/low-workbench.new /usr/local/bin/low-workbench
mv -f /usr/local/bin/low-standin.new /usr/local/bin/low-standin

# TLS: a key and a signing request for the internal CA, made here once.
# The certificate itself is signed elsewhere and installed as low.crt.
if [ ! -f "$SSL/low.key" ]; then
	say "creating TLS key and signing request for $HOST"
	install -d -m 0755 "$SSL"
	(umask 077; openssl req -new -newkey rsa:2048 -nodes -keyout "$SSL/low.key" -out "$SSL/low.csr" \
		-subj "/O=home.arpa/CN=$HOST" -addext "subjectAltName=DNS:$HOST" 2>/dev/null)
	chmod 0644 "$SSL/low.csr"
fi
if [ -f "$SSL/low.crt" ]; then
	SCHEME=https
else
	SCHEME=http
fi

# Settings. The base address follows whether a certificate is installed;
# everything else in the file is left as it is.
install -d -m 0750 -o root -g low /etc/low
ENV=/etc/low/workbench.env
if [ ! -f "$ENV" ]; then
	say "creating $ENV"
	cat > "$ENV" <<EOF
# Workbench settings, see /etc/systemd/system/low-workbench.service.
LOW_BASE_URL=$SCHEME://$HOST
# LOW_SMTP=mail.example.org:587
# LOW_SMTP_FROM=workbench@example.org
# LOW_SMTP_USER=
# LOW_SMTP_PASSWORD=
EOF
fi
sed -i "s#^LOW_BASE_URL=.*#LOW_BASE_URL=$SCHEME://$HOST#" "$ENV"
chown root:low "$ENV"
chmod 0640 "$ENV"

# Units.
install -m 0644 low-engine.service low-workbench.service /etc/systemd/system/
systemctl daemon-reload

# nginx: the HTTPS servers only once the certificate exists.
install -m 0644 low-workbench-proxy.conf /etc/nginx/snippets/low-workbench-proxy.conf
# Who may reach the site: the networks given at deploy time (allow.txt, one
# per line), always the host itself, nobody else.
{
	while read -r net; do
		[ -n "$net" ] && echo "allow $net;"
	done < allow.txt
	echo "allow 127.0.0.1;"
	echo "allow ::1;"
	echo "deny all;"
} > low-allow.conf
install -m 0644 low-allow.conf /etc/nginx/snippets/low-allow.conf
if [ "$SCHEME" = https ]; then
	sed '/#HTTP-ONLY-BEGIN/,/#HTTP-ONLY-END/d' low.home.arpa.conf > site.conf
else
	sed '/#HTTPS-BEGIN/,/#HTTPS-END/d' low.home.arpa.conf > site.conf
fi
install -m 0644 site.conf /etc/nginx/sites-available/$HOST
ln -sfn /etc/nginx/sites-available/$HOST /etc/nginx/sites-enabled/$HOST
nginx -t
systemctl reload nginx

# Start, or restart with the new programs.
systemctl enable --quiet low-engine.service low-workbench.service
systemctl restart low-engine.service
sleep 1
systemctl restart low-workbench.service

for i in $(seq 1 20); do
	if curl -fs -o /dev/null http://127.0.0.1:8110/healthz; then
		break
	fi
	sleep 0.5
done
curl -fsS -o /dev/null http://127.0.0.1:8110/healthz
curl -fsS -o /dev/null -H "Host: $HOST" http://127.0.0.1/ || curl -fsS -k -o /dev/null --resolve $HOST:443:127.0.0.1 https://$HOST/
say "running: $SCHEME://$HOST (engine: stand-in)"
if [ "$SCHEME" = http ]; then
	say "no certificate yet: sign $SSL/low.csr with the internal CA, install it as $SSL/low.crt, deploy again"
fi
