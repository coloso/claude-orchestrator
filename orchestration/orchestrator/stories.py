"""Story branches: a plan's tasks are merged into story/<plan>, never into your checkout.

Each plan gets an integration worktree (.worktrees/_story-<plan>) on branch story/<plan>,
created from the branch you ran `make run` on. Task worktrees start from the story branch,
and passing tasks are merged into it one at a time. When every task of the plan is done,
the combined result is checked with all gates, the integration worktree is removed (so you
can check the branch out yourself), and the branch is ready for you to review and push.
"""
from __future__ import annotations

import json
import time
from fnmatch import fnmatch
from pathlib import Path

import tasks
import worktrees
from common import ORCH, TOOLING, WORKTREES, emit, git, sh, tool_env

STORIES_DIR = ORCH / "stories"
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
    for f in sorted((TOOLING / "plans").glob("*.json")):
        try:
            if any(t.get("id") == root for t in json.loads(f.read_text())):
                return f.stem
        except (json.JSONDecodeError, AttributeError):
            continue
    return DEFAULT_PLAN


def branch(plan: str) -> str:
    return f"story/{plan}"


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
    git("worktree", "remove", "--force", str(wt), check=False)
    git("worktree", "prune", check=False)
    summary = {
        "branch": b,
        "base": base,
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
    emit("story_complete", task=None, detail=f"{b}: {summary['commits']} commits, {summary['files']} files, "
                                              f"checks {'skipped' if not run_checks else 'passed' if summary['ok'] else 'FAILED'}")
    return summary


def review_commands(plan: str) -> str:
    s = info(plan)
    b, base = branch(plan), s.get("base", "main")
    return (f"git log --oneline --graph {base}..{b}\n"
            f"git diff --stat {base}...{b}\n"
            f"git switch {b} && make check      # try it, e.g. make up\n"
            f"git push -u origin {b}            # then open a pull request")


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
    print("Review and push it yourself:\n" + "\n".join("  " + l for l in review_commands(plan).splitlines()))
