# One planner across repos: pools keyed by repo (JOB-207)

2026-11-12. Follows `184-pack-prefix.md`, which gave each repo its own
pools and planner.

**Pools keyed by repo.** A pool takes `repos:` next to its default
`repo:`:

    pools:
      dev:
        repo: /src/widget          # default checkout
        repos:
          acme/gizmo: {checkout: /src/gizmo, ref: origin/main}

Each session carries a repo key (`SessionFields.Repo`, "" for the
default). A keyed session claims only tickets whose `meta.github.repo`
matches its key (case-insensitive). It takes its worktree, and its
sandbox's git dir, from that checkout. A default session claims every
other ticket. Held and pre-assigned tickets are not narrowed. Scaling
keeps one desired count per pool (`max_sessions` caps the total) and
splits it over the keys by demand (ready plus working), one at a time.
A key with no work loses its sessions to scale cordons, and a key
dropped from the config drains (`scaleplan.ForKeys`). `session new`
always starts a default session.

**Packs.** A CUE `packs:` entry takes `pools_from: <other entry>`. Its
pools and every pool reference take the other entry's prefix instead
of its own. A pool it declares must be one of the other entry's, and
that entry must not use `pools_from` itself. `pool_prefix` is the prefix
pools carry, so prompts write `--pool {{.pool_prefix}}dev`. In packs/github
with `pools_from` set:

- the entry declares only `repos: {<repo>: {checkout}}` on dev,
  dev-sonnet, triage, refinery and deploy-check (not plan);
- it has no `replan` trigger and no store-wide scan signals: the other
  entry's cover both repos;
- it adds `shared-plan` (plan pool) and `shared-repo` (tickets of its
  repo) prompt rules, which name its repo, workflows and check command;
- its workflows, repo triggers and `issue_no_run` stay per repo.

    packs: gizmo: githubpack.#Pack & {
        prefix: "gz-", pools_from: "github"
        vars: {repo: "acme/gizmo", checkout: "/src/gizmo", agent: "a", human: "wile"}
    }

**YAML packs lists (JOB-232, 2026-11-18).** A `packs:` list entry in
jobshop.yaml takes `pools_from` too. It names the other entry by its
prefix without the trailing `-`/`_`, or by its pack directory when it has
no prefix; the owner may come later in the list. Rules are the CUE
ones (no chain, only the owner's pools). A pool's `repos:` keys render
`{{.var}}` in YAML packs, so the entry can add its repo from a var.

**`session new --repo <owner/name>`** (JOB-232) starts a session keyed
to a `repos:` entry of `--pool`, matched case-insensitively. A key the pool
lacks is refused. Without it the session is a default one. A ticket
filed by hand still needs `--meta '{"github":{"repo":...}}'` for the
keyed session to claim it.

Tests: `TestGithubPackSharedPools`, `TestPoolKeyedByRepo`, `TestForKeys`,
storetest `ClaimNextRepo`, `TestPackPoolsFromYAML`, `TestSessionNewRepo`.
