# Updating a mesh

[English](updating.md) · [Русский](ru/updating.md)

The server and its nodes update separately: the server replaces its own binary, and a node installs the
release its server reports. The server always goes first. A half-updated mesh keeps carrying traffic —
`api_version` is 1 in both 0.6.0 and 0.7.0.

`termit-mn update` and `termit-uv upgrade` arrived in 0.7.0, so a 0.6.0 install does not have them yet.
Getting to 0.7.0 means running the installer scripts again, this time with `--version v0.7.0`. From
0.7.0 on, those two commands do the same job.

0.5.1 and 0.5.2 take the same path as 0.6.0. Their configuration is a subset of 0.7.0's (the newer keys
are optional), and the state schema is 1 in all of them, so nothing has to be edited by hand. Both were
taken to 0.7.0 on the lab with their identities and client links unchanged.

## From 0.6.0 to 0.7.0

### The server

```bash
wget -qO mn.sh https://github.com/shogun-kub/termit/releases/download/v0.7.0/mn.sh
sh mn.sh --version v0.7.0
termit-mn version
termit-mn status
```

`mn.sh` checks the binary against the release's `SHA256SUMS`, replaces `/usr/bin/termit-mn`, runs `init`
against the existing configuration and restarts the unit. The database, the master key, the admin token
and the certificate stay as they are. If this server matters, run `termit-mn backup` first — the archive
lands in `/var/lib/termit/mn/backups/`.

### Each node

```bash
wget -qO uv.sh https://github.com/shogun-kub/termit/releases/download/v0.7.0/uv.sh
sh uv.sh --version v0.7.0 --no-enroll
termit-uv version
termit-uv doctor
termit-uv sync
```

`--no-enroll` keeps the node in the mesh. `init` rewrites what the configuration asks for — units, vhost,
core config, decoy — and never regenerates the identity, the certificate, the credentials or the client
links, so the phones keep working. `termit-uv sync` then tells the server which build the node runs; the
mesh view reads it from the sync.

### If the firewall table is missing UDP/443

`uv.sh` warns about this when `init` renders a table that the applied one does not match: the table in
force was rendered before Hysteria2 was turned on, and a reboot restored it from an older record. A
missing `udp dport 443` is not just a closed port: the QUIC packets of every client land in the scan
meter, the guard bans the client, and even the TLS profile stops working.

Fix it on the node:

```bash
termit-uv firewall status      # what is in force, and which fields moved
termit-uv firewall on          # render the table from the configuration
termit-uv firewall confirm     # keep it — without this it rolls back in five minutes
```

`on` applies the table for five minutes and prints a reminder to check that SSH and 443 still answer;
`confirm` keeps it. A confirmed table is also what the next reboot restores, so the warning goes away with
it. If a client is already banned, lift the ban by address:

```bash
termit-uv bans list            # a "scan" ban is a client the table did not let in
termit-uv bans unban <ip>
```

The full procedure is in [the runbook](../RUNBOOK.md) §12 step 8 and §13.

### Check the mesh

```bash
termit-mn uv list      # every node: termit-uv v0.7.0
```

## From now on

Both sides update themselves. The server first:

```bash
termit-mn update --check   # is this server behind the newest release?
termit-mn update --apply   # download, verify, swap, restart
```

then every node:

```bash
termit-uv sync            # learn the server's build
termit-uv upgrade --check # what a run would install
termit-uv upgrade         # download, verify, swap, converge
termit-uv sync            # tell the server what the node now runs
```

The `AGENT` column of `termit-mn uv list` takes the new build from the node's sync — that is why the block
ends with one. `--apply` keeps the old binary at `/usr/bin/termit-mn.prev` and restores it if the restart
fails. `upgrade` does the same with `termit-uv.prev`, then lets the new binary run `init`, because the new
release knows what it needs. A node on a pre-release build is refused on purpose: name the release
(`termit-uv upgrade --to v0.7.0`) or run the script again — a node follows releases, never `p<n>` builds.

## If it goes wrong

- **A node is worse after the update.** Put the previous release back with the same script:
  `sh uv.sh --version v0.6.0 --no-enroll`. The state schema is 1 in both, and the configuration is read
  the same way, so the older binary loads what the newer one wrote.
- **The server does not come back after `mn.sh`.** Run `systemctl restart termit-mn` and read the journal.
  If the database moved forward, restore the backup taken before the update (`termit-mn restore`).
- **`termit-mn update` or `termit-uv upgrade` is unknown.** That binary is 0.6.0 or older. Use the scripts,
  as above.
