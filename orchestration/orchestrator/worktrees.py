"""One git worktree + branch per task: .worktrees/T-001 on branch agent/T-001."""
from __future__ import annotations

import shutil
from pathlib import Path

from common import ROOT, WORKTREES, git


def branch_for(task_id: str) -> str:
    return f"agent/{task_id}"


def path_for(task_id: str) -> Path:
    return WORKTREES / task_id


def create(task_id: str, base: str) -> Path:
    """Fresh worktree from the current base branch (drops any earlier attempt)."""
    remove(task_id)
    wt = path_for(task_id)
    git("worktree", "add", "-b", branch_for(task_id), str(wt), base)
    link_shared_deps(wt)
    return wt


def remove(task_id: str) -> None:
    wt = path_for(task_id)
    if wt.exists():
        git("worktree", "remove", "--force", str(wt), check=False)
        shutil.rmtree(wt, ignore_errors=True)
    git("worktree", "prune", check=False)
    git("branch", "-D", branch_for(task_id), check=False)


def commit_all(wt: Path, message: str) -> bool:
    """Commit everything the agent changed. Returns False if there was nothing to commit."""
    git("add", "-A", cwd=wt)
    if git("diff", "--cached", "--quiet", cwd=wt, check=False).returncode == 0:
        return False
    git("-c", "user.name=orchestrator", "-c", "user.email=orchestrator@localhost",
        "commit", "-m", message, cwd=wt)
    return True


def changed_files(task_id: str, base: str) -> list:
    out = git("diff", "--name-only", f"{base}...{branch_for(task_id)}").stdout
    return [line for line in out.splitlines() if line]


def link_shared_deps(wt: Path) -> None:
    # node_modules is gitignored, so share the main checkout's install instead of reinstalling.
    src = ROOT / "frontend" / "node_modules"
    dst = wt / "frontend" / "node_modules"
    if src.exists() and dst.parent.exists() and not dst.exists():
        dst.symlink_to(src)
