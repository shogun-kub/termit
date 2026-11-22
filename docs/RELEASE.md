# Termit — the release process

`main` is what a participant installs; nothing else is. This document is the process that makes the tree
behind a tag one that has already passed CI and the lab, and the checklist that runs before every tag.
`AGENTS.md` §9 points here.

## 1. Branches

| Branch | Purpose | CI |
|---|---|---|
| `main` | stable releases only; receives merges from `release/<version>`, nothing else | build + tests, never publishes |
| `dev` | current development | build + tests, never publishes |
| `release/<version>` | stabilisation for one release, cut from `dev` | build + tests; the only branch a pre-release can be built from |

- **No direct commits to `main`.** Work lands on `dev` and reaches `main` by merging `release/<version>`.
- **`release/<version>`** is created from `dev` when a release is planned; the branch name **is** the
  version (`release/v0.2.0`). Cherry-picks from `dev` and small fixes committed on the branch are
  allowed. There is no version file to edit — `deploy/release --version v<version>` stamps the binaries,
  so the branch name, the tag and `--version` must agree.
- **`push --force` is forbidden on `main` and `dev`.** Do not amend someone else's commit.

## 2. Pre-release

Started by hand: `workflow_dispatch` of `.github/workflows/release.yaml` on a `release/<version>` branch
with `publish_prerelease` set. It builds both binaries, `uv.sh`, `mn.sh`, the units and `SHA256SUMS`, and
may publish them as a pre-release named **`v<version>p<n>`** — `v0.3.2p1` is the first build of `0.3.2`
from its release branch, `v0.3.2p2` the second, and `n` never resets for a version: the existing tags are
the counter. Nothing is published from `dev` or `main`. The artifacts carry the pre-release version
itself, so `uv.sh --version v0.3.2p1` installs exactly that build, and the changelog section of `0.3.2`
supplies its notes.

## 3. Final release

Merge `release/<version>` into `dev` and into `main`, delete the branch, then create an annotated tag
`v<version>` on `main`. Pushing the tag runs the final build and publishes the GitHub Release. The
release stays marked pre-release until that release's own production pass is recorded in
`docs/IMPLEMENTATION_PLAN.md` (§3.3.1 for `0.2.1`, §3.4.11 for `0.3.2`); flipping the flag is a
deliberate, recorded act.

**Flipping `prerelease` does not move the "latest" pointer.** A release published as a pre-release keeps
`make_latest` on the previous one, so `releases/latest` — and therefore `uv.sh` and `mn.sh` without
`--version` — go on offering the old build. Patch the release with `make_latest: "true"` after the flip;
the API answers immediately and the `github.com/.../releases/latest` redirect lags minutes behind it while
its cache expires.

**A tag points at a commit that has already been tested.** Retagging is allowed only while the release is
unpublished; `v0.1.0` is published and stays where it is. Never retag a released commit — a version that
means two different trees is worse than a wrong one.

## 4. The release body

The body is the changelog, from that version back to the last release. `go run ./deploy/release --version
v<version> --notes` prints the section; for a release (never a `v<version>p<n>` build) `--since <last
release>` prints every section down to, but not including, it, because the versions in between existed
only as pre-releases and whoever installs this one skipped them. The section must exist in `CHANGELOG.md`
and `CHANGELOG.ru.md` with a real date before tagging: the tool refuses a missing section, one that still
says `unreleased` and a placeholder date.

## 5. What must be proved before a tag

- **A release that touches the network is proved on production.** Anything under `internal/uv/xray`,
  `internal/uv/warp` or the network half of `internal/uv/install` depends on networks we do not control.
  A pre-release goes onto the production nodes first, and what is observed there is the egress the node
  really uses — `warp status`, the tunnel's exit, what a client sees — not the one the code intends; only
  then does the tag exist. Local code (decoy, terminal, docs) may ship on CI and the lab alone.
- **An accepted advisory is a debt with a due date.** `govulncheck` has no exclusion flag, so
  `deploy/vulngate` is the gate: it fails on any finding not named in `deploy/vulnerabilities.json` and
  prints the action for each entry. An entry carries `must_be_gone_by` — the first release that must not
  see it — and `deploy/release` refuses that version while it is there. **Before every release: update
  the module, delete the entry.** Moving the deadline is a deliberate act with a reason in the commit,
  not a way to silence the reminder.

## 6. Checklist before a release

Every item here exists because it was missed once. Run them in order; the ones with a command are meant
to be run, not remembered.

**1. Freeze the scope.** `git log --oneline v<last>..dev` and make every commit appear in this release's
section of `CHANGELOG.md` — and in `CHANGELOG.ru.md`, same facts, same order. The tool refuses a missing
section, a section that still says `unreleased`, and a placeholder date.

**2. Document what changed, in every place it shows.** A feature is not done until its surface is written
down; the surface is wider than the command:

| Added or changed | Also update |
|---|---|
| a command, a flag, a default | the `<binary> --help` summary line and its command block, the `docs/guide/using-*.md` table, the `docs/OPERATIONS.md` reference |
| a menu entry, a picker, a prompt, a label | `docs/guide/menu.md` (en and ru), `internal/i18n` for both languages, the screen mock in that guide if it shows the line |
| a config key or its default | the struct comment, `DESCRIPTION.md`, `OPERATIONS.md`, the guide's row |
| a rendered page or a printed block | the sample in the guide and `README` if it carries one |
| a port, a unit, an outbound host | `OPERATIONS.md`'s service table and `DESCRIPTION.md` §5.1, and `AGENTS.md` §2.3 if the host is new |
| an error a person may see | `docs/guide/troubleshooting.md` in both languages |

`--help` summaries are code and nobody reads them until an operator does: `invite` and `uv` carried
incomplete ones through a whole release.

**3. Make the README true.** `README.md` and `README.ru.md` are the project's face and the first thing
anyone reads: they name the newest release, list what works, and say what does not. Before tagging, read
both end to end against the release — the status line names this version and nothing later, every feature
claimed is in this release and not the next one, no claim is unverified (a pass that was never recorded
does not count), the quick-start commands run as written, "what is planned" is still planned and not
described as shipped, and the two languages say the same thing. A release left the README naming a version two releases back: it is a
five-minute read and the only document a stranger judges us by.

**4. Grep for the truth you just invalidated.** For every rename, removal or reversed decision, search the
tree, not the diff: `rg 'warp\.family|status and doctor|§1[01]\.' docs README* AGENTS.md`. Every hit is
either updated or still true, and the answer goes in the commit message if it is not obvious. The audit
after `0.7.0` found five stale claims that no test could see — a key documented as ignored when the loader
refused it, a diagnostic that did not exist, a forwarded command that could not work.

**5. Renumbering is a change to every document that points at you.** Moving a section in
`docs/IMPLEMENTATION_PLAN.md` breaks `§x.y` in `DESCRIPTION.md`, `OPERATIONS.md`, `RUNBOOK*.md`,
`QUESTIONS.md`, `SPIKES.md`, `AGENTS.md` and in the plan's own prose. Keep the old→new map, then
`rg '§\d' docs README* AGENTS.md` and fix each hit.

**6. Prove it on a machine, not only in tests.** Build both binaries for `linux/amd64` with `-ldflags`
version stamping, copy them to the lab, run the scenario, then put the lab back on a normal build. The
`0.7.0` run paid for itself twice: `upgrade` converged the node with `init` and no `--config` (the
rollback kept it serving), and the server's update asked `api.github.com`, which answered `403` where
`github.com` worked. Neither was reachable from a unit test, and both would have shipped.

**7. Prove the way back.** For anything that replaces a binary, a config or a unit: break the release on
purpose (a corrupted `SHA256SUMS`, a bad asset), run the swap, and watch the previous file return and the
service stay up. An unproven rollback is a rumour; the lab is where it is cheap.

**8. Prove the network on production.** A change under `internal/uv/xray`, `internal/uv/warp` or the
network half of `internal/uv/install` is not released on the lab's word: install the pre-release on the
production nodes, read `warp status`, the tunnel's exit and what a client actually sees, and record the
pass. §5 above says what counts as recorded.

**9. Make the versions agree.** Branch name, tag, `deploy/release --version` and the changelog heading are
one version; `p<n>` continues from the existing tags, never resets; `--notes --since <last release>`
prints every section since the last release, because the pre-releases in between were skipped by whoever
installs this one. Print the notes and read them before publishing.

**10. Run the gate deliberately.** `go run ./deploy/vulngate ./cmd/... ./internal/...`, then
`go run ./deploy/release --version v<version> --notes`. It refuses while an accepted advisory names this
version; clear it by moving the dependency, or move `must_be_gone_by` in a commit that says why.

**11. Publish, then move the pointer.** Tag `main`, publish, and check the release body. When the
production pass exists, flip `prerelease` **and** set `make_latest` — a flipped pre-release otherwise
leaves `releases/latest`, and therefore `uv.sh` and `mn.sh` without `--version`, on the previous build.

**12. Leave nothing behind.** No `<binary>.prev` on a machine you return to service, no lab host on a dev
build, no secret or address in a document, `go run ./deploy/clean` after the push, and the next release's
section absent from the changelog until it is written.
