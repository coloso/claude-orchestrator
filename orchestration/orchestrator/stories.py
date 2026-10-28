"""Story branches: a plan's tasks are merged into story/<plan>, never into your checkout.

Each plan gets an integration worktree (.worktrees/_story-<plan>) on branch story/<plan>,
created from the branch you ran `make run` on. Task worktrees start from the story branch,
and passing tasks are merged into it one at a time. When every task of the plan is done:

1. the combined result is checked with all gates;
2. the lead engineer agent reviews the whole change (verdict, report, follow-up proposals,
   commit message);
3. the integration worktree is removed (so you can check the branch out yourself);
4. the story is squashed into one commit on pr/<plan>, ready to push as a pull request.
   story/<plan> keeps the per-task history;
5. with config "merge_into_base", an approved story with passing checks is fast-forwarded onto
   the branch it started from (usually the one you have checked out). Never a merge commit.

Approving a proposal reopens the story; finishing it again re-runs the review and the squash.
"""
from __future__ import annotations

import json
import re
import shlex
import subprocess
import time
from fnmatch import fnmatch
from pathlib import Path

import mcp
import tasks
import worktrees
from common import (LOGS_DIR, MCP_DIR, ORCH, PLANS_DIR, ROLES_DIR, ROOT, STORY_FILES_DIR, WORKTREES, emit, git,
                    model_args, sh, tool_env)

STORIES_DIR = ORCH / "stories"
REVIEWS_DIR = ORCH / "reviews"
DEFAULT_PLAN = "adhoc"
# Statuses that keep a story's integration worktree busy. Proposals don't: they wait for a
# human, who should be able to check out and review the story branch meanwhile.
ACTIVE = ("pending", "claimed", "passed", "failed")


def plan_of(task: dict) -> str:
    """The plan a task belongs to. Tasks queued before plans were recorded are looked up in the plan
    files by their ID (a proposal like S01-08.1 belongs to the plan of S01-08)."""
    if task.get("plan"):
        return task["plan"]
    root = task["id"].split(".")[0]
    for f in sorted(PLANS_DIR.glob("*.json")):
        try:
            if any(t.get("id") == root for t in json.loads(f.read_text())):
                return f.stem
        except (json.JSONDecodeError, AttributeError):
            continue
    return DEFAULT_PLAN


def branch(plan: str) -> str:
    return f"story/{plan}"


def pr_branch(plan: str) -> str:
    return f"pr/{plan}"


def plans_based_on(base: str) -> set:
    """Plans whose story branch was started from `base` (the branch `make run` ran on)."""
    return {p.stem for p in STORIES_DIR.glob("*.json") if info(p.stem).get("base") == base}


def worktree_path(plan: str) -> Path:
    return WORKTREES / f"_story-{plan}"


def info(plan: str) -> dict:
    f = STORIES_DIR / f"{plan}.json"
    return json.loads(f.read_text()) if f.exists() else {}


def record(plan: str, **fields) -> None:
    STORIES_DIR.mkdir(parents=True, exist_ok=True)
    (STORIES_DIR / f"{plan}.json").write_text(json.dumps({**info(plan), "plan": plan, **fields}, indent=2))


def branch_exists(name: str) -> bool:
    return git("rev-parse", "--verify", "--quiet", f"refs/heads/{name}", check=False).returncode == 0


def ensure(plan: str, base: str) -> Path:
    """The plan's integration worktree, created (with its branch, from `base`) if needed."""
    wt = worktree_path(plan)
    if wt.exists():
        return wt
    git("worktree", "prune", check=False)
    b = branch(plan)
    if branch_exists(b):
        git("worktree", "add", str(wt), b)
    else:
        git("worktree", "add", "-b", b, str(wt), base)
        record(plan, branch=b, base=base, created_at=time.time())
    worktrees.link_shared_deps(wt)
    record(plan, completed_at=None)
    emit("story_open", task=None, detail=f"{b} in {wt.name}")
    return wt


def merge(task: dict, base: str) -> tuple:
    """Merge a passed task branch into its story branch. Returns (ok, output)."""
    plan = plan_of(task)
    wt = ensure(plan, info(plan).get("base") or base)
    result = git("merge", "--no-ff", "-m", f"Merge {task['id']}: {task['title']}",
                 worktrees.branch_for(task["id"]), cwd=wt, check=False)
    if result.returncode != 0:
        git("merge", "--abort", cwd=wt, check=False)
        return False, (result.stdout + result.stderr)[-800:]
    return True, ""


def open_plans() -> set:
    return {plan_of(t) for status in ACTIVE for t in tasks.list_tasks(status)}


def completable_plans() -> list:
    """Plans with an integration worktree and no task left to run (proposals may still wait)."""
    still_open = open_plans()
    return sorted(p.name[len("_story-"):] for p in WORKTREES.glob("_story-*")
                  if p.is_dir() and p.name[len("_story-"):] not in still_open)


def finalize(plan: str, config: dict, run_checks: bool = True) -> dict:
    """Check the combined result, release the worktree, and record a summary."""
    wt, b = worktree_path(plan), branch(plan)
    base = info(plan).get("base", "main")
    changed = [f for f in git("diff", "--name-only", f"{base}...{b}", check=False).stdout.splitlines() if f]
    checks = []
    for gate in config["gates"] if run_checks else []:
        if not any(fnmatch(f, pat) for f in changed for pat in gate["paths"]) or not (wt / gate["cwd"]).exists():
            continue
        print(f"  checking {b}: {gate['name']} gate ...", flush=True)
        r = sh(gate["cmd"], cwd=wt / gate["cwd"], check=False, env=tool_env())
        checks.append({"gate": gate["name"], "ok": r.returncode == 0,
                       "output": (r.stdout + r.stderr).strip()[-3000:]})
    review = lead_review(plan, wt, base, checks, config) if run_checks else {}
    git("worktree", "remove", "--force", str(wt), check=False)
    git("worktree", "prune", check=False)
    pr = squash(plan, base, review.get("commit_title"), review.get("commit_body"))
    merged = {}
    if config.get("merge_into_base") and run_checks and pr.get("pr_commit") and not pr.get("pr_error"):
        merged = ({"merge_skipped": "the lead engineer did not approve the story"} if review.get("verdict") != "approve"
                  else {"merge_skipped": "checks failed"} if not all(c["ok"] for c in checks)
                  else merge_into_base(pr["pr_branch"], base))
    summary = {
        "branch": b,
        "base": base,
        "review": {k: v for k, v in review.items() if k not in ("commit_title", "commit_body")},
        **pr,
        "merged_into": merged.get("merged_into"),
        **({"merged_commit": merged["merged_commit"]} if merged.get("merged_commit") else {}),  # kept until the next merge
        "merge_skipped": merged.get("merge_skipped"),
        "head": git("rev-parse", "--short", b, check=False).stdout.strip(),
        "commits": int(git("rev-list", "--count", f"{base}..{b}", check=False).stdout.strip() or 0),
        "files": len(changed),
        "checks": checks,
        "ok": all(c["ok"] for c in checks),
        "checked": run_checks,
        "completed_at": time.time(),
    }
    summary["proposals_waiting"] = [t["id"] for t in tasks.list_tasks("proposed") if plan_of(t) == plan]
    record(plan, **summary)
    verdict = review.get("verdict")
    emit("story_complete", task=None, detail=f"{b}: {summary['commits']} commits, {summary['files']} files, "
                                              f"checks {'skipped' if not run_checks else 'passed' if summary['ok'] else 'FAILED'}"
                                              + (f", lead: {verdict}" if verdict else "")
                                              + (f"; squashed into {pr['pr_branch']}" if pr.get("pr_commit") else ""))
    return summary


def plan_tasks(plan: str) -> list:
    """[(status, task)] of a plan, in ID order."""
    return sorted(((st, t) for st, t in tasks.all_tasks().values() if plan_of(t) == plan), key=lambda x: x[1]["id"])


def story_title(plan: str) -> str:
    f = STORY_FILES_DIR / f"{plan}.md"
    if f.exists():
        for line in f.read_text().splitlines():
            if line.startswith("# "):
                return line[2:].strip()
    return plan


def lead_review(plan: str, wt: Path, base: str, checks: list, config: dict) -> dict:
    """Let the lead engineer review the whole story. Returns {verdict, summary, report, proposals,
    commit_title, commit_body, cost_usd} or {verdict: "error", summary} if the review failed."""
    lead = config["models"].get("lead")
    if not lead:
        return {}
    mb = git("merge-base", base, branch(plan), check=False).stdout.strip() or base
    story_file = STORY_FILES_DIR / f"{plan}.md"
    story = story_file.read_text() if story_file.exists() else f"(no story file; plan {plan})"
    merged = "\n".join(f"- {t['id']} [{st}] {t['title']}" for st, t in plan_tasks(plan) if st == "done")
    gates = "\n".join(f"- {c['gate']}: {'passed' if c['ok'] else 'FAILED'}"
                       + ("" if c["ok"] else f"\n```\n{c['output'][-1500:]}\n```") for c in checks) or "- none ran"
    prompt = (
        f"Story:\n\n{story}\n\n"
        f"Merged tasks:\n{merged or '- none'}\n\n"
        f"The story branch {branch(plan)} is checked out here. The change under review is "
        f"`git diff {mb}..HEAD` (it started from {base} at {mb[:10]}).\n\n"
        f"Quality gates on the combined branch:\n{gates}\n\n"
        "Review the whole change and return the JSON object."
    )
    servers, _ = mcp.servers_for("lead", config)
    cmd = [
        "claude", "-p", prompt,
        "--append-system-prompt", (ROLES_DIR / "lead.md").read_text(),
        *model_args(lead),
        "--allowedTools", *config["lead"]["allowed_tools"], *mcp.allowed_tools(servers),
        *mcp.claude_args(MCP_DIR / f"lead-{plan}.json", servers),
        "--output-format", "json",
    ]
    log = LOGS_DIR / f"lead-{plan}.json"
    print(f"  lead engineer reviewing {branch(plan)} with {lead['id']} (log: {log.relative_to(ROOT)}) ...", flush=True)
    emit("lead_review", task=None, detail=f"reviewing {branch(plan)}")
    try:
        r = subprocess.run(cmd, cwd=wt, env=tool_env(), text=True, capture_output=True, stdin=subprocess.DEVNULL,
                           timeout=config["lead"].get("timeout_s", 1800))
        log.write_text(r.stdout + r.stderr)
        out = json.loads(r.stdout)
        if out.get("is_error") or r.returncode != 0:
            raise RuntimeError(str(out.get("result") or f"exit code {r.returncode}")[:500])
        match = re.search(r"\{.*\}", out.get("result", ""), re.S)
        review = json.loads(match.group(0)) if match else None
        if not isinstance(review, dict) or review.get("verdict") not in ("approve", "changes_requested"):
            raise RuntimeError("no review JSON with a verdict in the lead's answer")
    except (subprocess.TimeoutExpired, json.JSONDecodeError, RuntimeError, OSError) as e:
        emit("lead_review_failed", task=None, error=str(e)[:500])
        return {"verdict": "error", "summary": f"Lead review failed: {e}"[:600], "log": log.relative_to(ROOT).as_posix()}

    REVIEWS_DIR.mkdir(parents=True, exist_ok=True)
    report = REVIEWS_DIR / f"{plan}.md"
    report.write_text(f"# Lead review: {plan}\n\n**Verdict:** {review['verdict']}\n\n"
                      f"{review.get('summary', '')}\n\n{review.get('report', '')}\n")
    proposals = review.get("proposals") or []
    ids = []
    if proposals:
        # Proposals are numbered under a pseudo task of the plan, e.g. S01-LEAD.1, S01-LEAD.2, ...
        prefix = next((t["id"].split("-")[0] for _, t in plan_tasks(plan)), plan)
        ids, errors = tasks.propose({"id": f"{prefix}-LEAD", "plan": plan}, proposals)
        for tid in ids:
            emit("task_proposed", task=tid, detail="proposed by the lead engineer")
        for err in errors:
            emit("proposal_invalid", task=f"{prefix}-LEAD", error=err)
        tasks.auto_decide(ids, config)
    emit("lead_reviewed", task=None, detail=f"{branch(plan)}: {review['verdict']}"
                                            + (f", {len(ids)} proposal(s)" if ids else ""))
    return {
        "verdict": review["verdict"],
        "summary": str(review.get("summary", ""))[:1000],
        "report": report.relative_to(ROOT).as_posix(),
        "proposals": ids,
        "cost_usd": out.get("total_cost_usd"),
        "commit_title": str(review.get("commit_title") or "").strip()[:100],
        "commit_body": str(review.get("commit_body") or "").strip(),
    }


def squash(plan: str, base: str, title: str = None, body: str = None) -> dict:
    """Put the whole story into one commit on pr/<plan>, on top of where the story started.
    The commit has story/<plan>'s exact tree, so nothing is re-merged; story/<plan> is left as it is."""
    b, pr = branch(plan), pr_branch(plan)
    mb = git("merge-base", base, b, check=False).stdout.strip()
    # A reopened story whose last commit is already on `base` (and nothing came after it) continues
    # from there, so the new commit can be fast-forwarded too.
    rec, tip = info(plan), git("rev-parse", base, check=False).stdout.strip()
    if any(c and tip.startswith(c) for c in (rec.get("merged_commit"), rec.get("pr_commit"))):
        mb = tip
    if not mb or not git("diff", "--name-only", f"{mb}..{b}", check=False).stdout.strip():
        return {}
    if not title:
        title = story_title(plan)
        body = "\n".join(f"- {t['title']}" for st, t in plan_tasks(plan) if st == "done")
    tree = git("rev-parse", f"{b}^{{tree}}").stdout.strip()
    sha = sh(["git", "commit-tree", tree, "-p", mb, "-F", "-"], input=f"{title}\n\n{body or ''}".strip() + "\n",
             check=False).stdout.strip()
    if not sha:
        return {"pr_branch": pr, "pr_error": "git commit-tree failed"}
    # Fails only if pr/<plan> is checked out somewhere; then the commit is still reported.
    moved = git("branch", "-f", pr, sha, check=False)
    return {"pr_branch": pr, "pr_commit": sha[:10], "pr_title": title,
            **({"pr_error": moved.stderr.strip()[-300:]} if moved.returncode else {})}


def merge_into_base(pr: str, base: str) -> dict:
    """Fast-forward `base` to pr/<plan>. Returns {"merged_into": base} or {"merge_skipped": reason}.
    Git refuses (and changes nothing) if your uncommitted or untracked files are in the way."""
    if git("merge-base", "--is-ancestor", base, pr, check=False).returncode:
        return {"merge_skipped": f"{base} has commits that are not in the story; merge {pr} yourself"}
    checked_out = [l.removeprefix("branch refs/heads/") for l in git("worktree", "list", "--porcelain").stdout.splitlines()
                   if l.startswith("branch ")]
    if git("rev-parse", "--abbrev-ref", "HEAD").stdout.strip() == base:
        r = git("merge", "--ff-only", "--quiet", pr, check=False)
    elif base in checked_out:
        return {"merge_skipped": f"{base} is checked out in another worktree"}
    else:
        r = git("update-ref", f"refs/heads/{base}", git("rev-parse", pr).stdout.strip(),
                git("rev-parse", base).stdout.strip(), check=False)
    if r.returncode:
        return {"merge_skipped": (r.stderr or r.stdout).strip()[-300:]}
    emit("story_merged", task=None, detail=f"{pr} fast-forwarded onto {base}")
    return {"merged_into": base, "merged_commit": git("rev-parse", "--short=10", pr).stdout.strip()}


def mainline() -> str:
    """The remote default branch (e.g. origin/main), or "" without a remote. A story counts as shipped
    once it is in there; local main doesn't count, since merge_into_base may have put it there unpushed."""
    head = git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD", check=False).stdout.strip()
    for ref in (head, "origin/main", "origin/master"):
        if ref and git("rev-parse", "--verify", "--quiet", ref, check=False).returncode == 0:
            return ref
    return ""


def pr_command(plan: str, head: str) -> str:
    """Push `head` and open a pull request into the mainline with the GitHub CLI. The lead's review
    becomes the PR description, so reviewers see it without access to this machine's .orch/."""
    s, base = info(plan), (mainline() or "origin/main").split("/", 1)[-1]
    report = (s.get("review") or {}).get("report")
    body = f"--title {shlex.quote(s.get('pr_title') or plan)} --body-file {report}" if report else "--fill"
    return (f"git push -u origin {head}\n"
            f"gh pr create --base {base} --head {head} {body}")


def review_commands(plan: str) -> str:
    s = info(plan)
    b, base = branch(plan), s.get("base", "main")
    rev = s.get("review") or {}
    if s.get("pr_commit"):
        pr = s["pr_branch"]
        return ((f"less {rev['report']}            # the lead's review\n" if rev.get("report") else "")
                + f"git show --stat {pr}                # the story as one commit\n"
                f"git log --oneline --graph {base}..{b}   # per-task history\n"
                f"git switch {pr} && make check      # try it, e.g. make up\n"
                + pr_command(plan, pr))
    return (f"git log --oneline --graph {base}..{b}\n"
            f"git diff --stat {base}...{b}\n"
            f"git switch {b} && make check      # try it, e.g. make up\n"
            + pr_command(plan, b))


def print_summary(plan: str) -> None:
    s = info(plan)
    status = ("checks skipped (dry run)" if not s.get("checked", True)
              else "all checks passed" if s.get("ok") else "CHECKS FAILED, see below")
    print(f"\nStory {plan} is complete on branch {s['branch']}: "
          f"{s['commits']} commits, {s['files']} files changed, {status}.")
    if s.get("proposals_waiting"):
        print(f"{len(s['proposals_waiting'])} proposed follow-up(s) wait for your decision "
              f"({', '.join(s['proposals_waiting'])}); approving one reopens the branch on the next run.")
    for c in s.get("checks", []):
        if not c["ok"]:
            print(f"--- {c['gate']} gate output (tail) ---\n{c['output'][-1500:]}\n---")
    rev = s.get("review") or {}
    if rev.get("verdict"):
        label = {"approve": "approved", "changes_requested": "CHANGES REQUESTED", "error": "review FAILED"}[rev["verdict"]]
        print(f"Lead engineer: {label}. {rev.get('summary', '')}")
        if rev.get("proposals"):
            print(f"  The lead proposed {len(rev['proposals'])} fix(es) ({', '.join(rev['proposals'])}): make review")
    if s.get("pr_commit"):
        print(f"Squashed into one commit on {s['pr_branch']} ({s['pr_commit']}): {s.get('pr_title', '')}")
    if s.get("pr_error"):
        print(f"Could not update {s.get('pr_branch')}: {s['pr_error']}")
    if s.get("merged_into"):
        print(f"Merged onto {s['merged_into']} (fast-forward). It is not pushed: git push")
        return
    if s.get("merge_skipped"):
        print(f"Not merged onto {s.get('base')}: {s['merge_skipped']}")
    print("Review and push it yourself:\n" + "\n".join("  " + l for l in review_commands(plan).splitlines()))
