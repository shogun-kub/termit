# Updating a mesh

Termit updates from the inside: the server replaces its own binary and a node takes the release its
server names. Those two commands — `termit-mn update` and `termit-uv upgrade` — are new in 0.7.0, so they
cannot install it: a mesh on 0.6.0 or older moves with the bootstrap scripts it was installed with. The
server goes first, and while you go a fleet that mixes 0.6.0 and 0.7.0 keeps working, because
`api_version` is 1 on both and the wire contract is additive.

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
and the certificate are left alone. Take a backup first if this server matters: `termit-mn backup` (the
archive lands in `/var/lib/termit/mn/backups/`).

### Each node

```bash
wget -qO uv.sh https://github.com/shogun-kub/termit/releases/download/v0.7.0/uv.sh
sh uv.sh --version v0.7.0 --no-enroll
termit-uv version
termit-uv doctor
termit-uv sync
```

`--no-enroll` keeps the enrollment. `init` rewrites what the configuration asks for — the units, the
vhost, the core config, the decoy — and never regenerates the identity, the certificate, the credentials
or the client links, so the phones keep working. `termit-uv sync` tells the server this node is now on
0.7.0: the mesh view reads the agent's build from the sync.

If `uv.sh` warns that the applied firewall table was rendered before UDP/443, the table is older than the
configuration: `termit-uv firewall status` names the fields that moved and `termit-uv firewall on`
renders it again.

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

then, on each node:

```bash
termit-uv sync            # learn the server's build
termit-uv upgrade --check # what a run would install
termit-uv upgrade         # download, verify, swap, converge
termit-uv sync            # tell the server what the node now runs
```

The `AGENT` column of `termit-mn uv list` takes the new build from the node's sync, which is why the block
ends with one. `--apply` keeps the running binary at `/usr/bin/termit-mn.prev` and restores it when the restart fails;
`upgrade` does the same with `termit-uv.prev` and lets the new binary run `init`, because the new release
is the one that knows what it needs. A node on a pre-release build is refused by design: name the release
(`termit-uv upgrade --to v0.7.0`) or re-run the script — a node follows releases, never `p<n>` builds.

## If it goes wrong

- **A node is worse after the update.** Put the previous release back with the same script:
  `sh uv.sh --version v0.6.0 --no-enroll`. The state schema is 1 in both releases and the configuration is
  read the same way, so the older binary loads what the newer one wrote.
- **The server does not come back after `mn.sh`.** `systemctl restart termit-mn`, read the journal; if the
  database moved forward, restore the backup taken before the update (`termit-mn restore`).
- **`termit-mn update` or `termit-uv upgrade` is unknown.** That binary is 0.6.0 or older — use the
  scripts, as above.
