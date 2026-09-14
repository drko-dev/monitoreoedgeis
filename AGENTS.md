# AGENTS.md — Operating rules for GEO CAM Edge

> **Core principle: the repository is the persistent memory of this project.**
> Conversational context with any AI is disposable. Everything that matters —
> state, decisions, backlog, validations — lives in Git. If it is not in the
> repo, it does not exist.

## Identity

| Field          | Value                                                            |
| -------------- | ---------------------------------------------------------------- |
| **PRODUCT**    | GEO CAM Edge                                                     |
| **REPO**       | `monitoreoedgeis`                                                |
| **CORE**       | Go                                                               |
| **TARGETS**    | Linux amd64, Linux arm64                                         |
| **DEV ENV**    | Mac + Rancher Desktop + K3s local + containerd                   |
| **PRODUCTION** | Separate VPS. **Do not touch without explicit authorization.**   |
| **SAAS REPO**  | `monitoreoia`                                                    |
| **EDGE REPO**  | `monitoreoedgeis` (this one)                                     |

Both repos may be opened side by side as sibling repos in one workspace, but
they keep **separate lifecycle, separate commits, separate PRs**.

## Recommended reading order to recover context

1. `AGENTS.md` (this file)
2. `docs/PROJECT_STATUS.md`
3. `docs/ARCHITECTURE.md`
4. `docs/ROADMAP.md`
5. `README.md`
6. The currently open PR

## Mandatory rules

- Do **not** modify the SaaS unless the task is explicitly cross-repo.
- Do **not** touch the VPS / production without explicit authorization.
- Every change is developed and tested **locally first**.
- Do **not** mix commits between repos.
- Do **not** require Docker Engine — it is not used here.
- Local K3s is the primary development environment.
- Preserve existing local work. No `git reset --hard`. No `git clean`.
  No force push.
- Do **not** merge a PR without explicit authorization from the user.
  "continuamos", "dale", "seguí" and similar **do not** mean permission to merge.
- Always distinguish:
  **IMPLEMENTED / TESTED / VALIDATED LOCAL / MERGED / DEPLOYED PROD.**
- Keep documentation updated when closing each milestone (hito).
- Never claim a validation that was not actually observed.

## Environment decisions

**All development happens locally first.**

- Local environment: Mac, Rancher Desktop, K3s, containerd.
- Docker Desktop / Docker Engine is **not** required.
- Edge namespace: `geocam-edge-dev`.
- The local SaaS runs separately and is not touched by Edge work.

Flow:

```
code -> tests -> build -> OCI image -> local K3s -> validation -> review
     -> (only then, with explicit authorization) production
```

**Production is not a testing environment.**

## Edge ↔ SaaS relationship

Two repositories: **EDGE = `monitoreoedgeis`**, **SAAS = `monitoreoia`**.
Independent lifecycle, independent commits, independent PRs.

When a change requires both repos:

1. Define the contract.
2. Document the contract.
3. Modify the Edge in its own repo.
4. Modify the SaaS in its own repo.
5. Separate tests.
6. Separate PRs.

No silent cross-repo changes.

## Documentation responsibilities

Each document has one responsibility. **Do not duplicate large blocks between
documents.**

| File                     | Responsibility                                |
| ------------------------ | --------------------------------------------- |
| `AGENTS.md`              | Relatively stable working rules                |
| `docs/ARCHITECTURE.md`   | Architecture and technical decisions           |
| `docs/ROADMAP.md`        | What we want to build, and block status        |
| `docs/PROJECT_STATUS.md` | The real state **right now**                   |
| `README.md`              | General entry point                            |

## AI session hygiene

Do **not** keep a single gigantic session for the whole development. The
repository holds the persistent state; the session holds only temporary context.

### `/compact`

If the tool supports `/compact`, use it periodically when context grows too
large. Goal: reduce context, keep performance, drop irrelevant conversation,
avoid unnecessary consumption, keep the decisions that matter.

**`/compact` does not replace documentation in Git.**

Before `/compact`:
- Make sure important decisions are documented.
- Be clear on the current task.
- Record blockers.
- Record the next step.

After `/compact`, verify: branch, `git status`, current task, DONE criteria.

### `/clear` between blocks

If the tool supports `/clear`, use it when one task/milestone ends and a
genuinely different block begins.

Before `/clear`, all important context must be persisted — at minimum:
`PROJECT_STATUS` updated, `ROADMAP` updated, architecture updated if decisions
were taken, branch/PR clear, blockers clear, next step clear. A fresh session
must be able to continue by reading the repo alone.

Do not `/clear` in the middle of a task without leaving a handoff.

### Context economy

Load only the context that enables concrete decisions.

- Do **not** explore whole files/repos "just in case".
- Prefer specific files, fragments, summaries, contracts, diffs, relevant logs.
- Avoid giant dumps, unnecessary full logs, re-reading the same file repeatedly,
  aimless exploration, copying the whole repo into context.
- Subagents also receive the minimum sufficient context.

### Executor / subagent policy

Do not delegate automatically. An executor can account for roughly **40% of
context/token consumption** — that is significant. Before delegating, ask
whether it actually adds value.

Use an executor/subagent when there is:
- a bounded implementation,
- separable research,
- independent validation,
- parallelizable tasks without conflict,
- specialized review.

Avoid a subagent when:
- the task is trivial,
- the main agent already has the context,
- explaining the context costs more than solving it,
- it would duplicate reading,
- it introduces inconsistencies,
- it would simply repeat the main work.

Small tasks: prefer the main agent. Large tasks: subagent with strict scope.

A subagent prompt must contain: objective, relevant files, boundaries,
prohibitions, DONE criteria, short report format.

The main agent keeps responsibility for architecture, integration, final
decisions, review and DONE. **Do not let the executor become the implicit owner
of the architecture.**

## Handoff between sessions

When closing each milestone, update `docs/PROJECT_STATUS.md` with: milestone
finished, branch, HEAD, PR, implemented code, tests, validations, merge status,
deployment status, blockers, real problems found, next milestone, things that
must not be repeated.

Goal: a new AI must be able to start a clean session without reconstructing any
historical conversation.
