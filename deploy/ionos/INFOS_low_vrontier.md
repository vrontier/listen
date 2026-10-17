# low.vrontier.org: admin step

Listening Observatory Workbench (https://low.vrontier.org) and its engine need one root step on this server. `questmaster` stages everything in `/home/questmaster/low-deploy/` (`scripts/deploy-ionos-workbench.sh`). Run one script there as root:

```sh
bash /home/questmaster/low-deploy/install-workbench.sh
```

It is idempotent: a program or unit is replaced, and its service restarted, only when it changed. It needs `listen-ingest`, which the Observatory's root step installs (`INFOS_listen_vrontier.md`), and stops with a message if it is not running.

**It touches no nginx file and no certificate.** The site for low.vrontier.org is the administrator's; the Workbench only listens on 127.0.0.1:8110.

## What the script changes

| Area | Change |
|---|---|
| Users | `listen-engine` (nologin, joins the group `listen-ingest`); with the Workbench also `low` (nologin, joins the group `listen-engine`). |
| Programs | `/usr/local/bin/listen-engine`; with the Workbench also `/usr/local/bin/low-workbench`. Static Go binaries. |
| systemd | `listen-engine.service`: sockets `/run/listen-engine/control.sock` (0660, group `listen-engine`, for the Workbench) and `internal.sock` (0600), data in `/var/lib/listen-engine`, settings in `/etc/listen-engine/engine.env` (made once, empty). Limits `Nice=10`, `CPUQuota=200%`, `MemoryMax=2G`, `TasksMax=512`. `low-workbench.service`: 127.0.0.1:8110, catalogue and data sets in `/var/lib/low`, limits `Nice=10`, `CPUQuota=100%`, `MemoryMax=512M`, `TasksMax=256`. Both hardened like the listeners (ProtectSystem=strict, no capabilities). |
| Workbench settings | `/etc/low/workbench.env` (root:low 0640), made once: `LOW_BASE_URL=https://low.vrontier.org`, `LOW_RESERVED_INPUTS`, and `LOW_KEYS_SECRET`, made here once with `openssl rand -base64 32`. The mail lines (`LOW_SMTP*`) follow every staging; the rest of the file is left as it is. Publishing (`LOW_PUBLISHING`) and members' language-model keys (`LOW_LLM_KEYS`) are off unless set to `on` there. `/etc/low/reserved-inputs` (made once, empty). |
| Master admin | When staged (`master-admin`): created once in the catalogue; no login link is printed, they log in by mail. |

Outbound connections: the engine reads stations through `listen-ingest` only. The Workbench sends mail through the staged mail server (login links, invitations).

## Back up

- `/var/lib/low/` (the catalogue `workbench.db` and the data sets),
- `/etc/low/`, and **`LOW_KEYS_SECRET` separately**: without it, the language-model keys members saved cannot be opened again,
- `/var/lib/listen-engine/` (the engine's memories and saved configuration; the Workbench sends its configuration again at start, so this is the smaller loss).

## Check

```sh
systemctl is-active listen-ingest listen-engine low-workbench
curl -s --unix-socket /run/listen-engine/control.sock http://engine/v1/engine | head -c 200; echo
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8110/healthz              # 200
journalctl -u low-workbench -n 5 --no-pager                                        # "engine: instance …, API 1.1"
```

## Undo

```sh
systemctl disable --now low-workbench      # the Workbench; its data stays in /var/lib/low
systemctl disable --now listen-engine      # the engine; its data stays in /var/lib/listen-engine
```

Users, programs and units can stay or go (`userdel low`, `userdel listen-engine`, `rm /usr/local/bin/{low-workbench,listen-engine} /etc/systemd/system/{low-workbench,listen-engine}.service`). The Observatory and `listen-ingest` are not affected.

Source: https://github.com/vrontier/listen (`workbench/`, `deploy/ionos/install-workbench.sh`, `scripts/deploy-ionos-workbench.sh`).
