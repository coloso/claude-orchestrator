"""File-based task queue.

Each task is a JSON file; its status is the directory it lives in:

    .orch/tasks/pending/  ->  claimed/  ->  passed/  ->  done/
                                  \\-> failed/ (requeued until max_attempts)

Claiming is an atomic os.rename, so two workers can never grab the same task.
Agents can propose follow-up tasks; they wait in proposed/ until a human approves them.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Optional

from common import STATUSES, TASKS_DIR


def path(status: str, task_id: str) -> Path:
    return TASKS_DIR / status / f"{task_id}.json"


def read(p: Path) -> dict:
    return json.loads(p.read_text())


def write(status: str, task: dict) -> None:
    path(status, task["id"]).write_text(json.dumps(task, indent=2))


def list_tasks(status: str) -> list:
    return [read(p) for p in sorted((TASKS_DIR / status).glob("*.json"))]


def all_tasks() -> dict:
    """{task_id: (status, task)} across every status directory."""
    out = {}
    for status in STATUSES:
        for t in list_tasks(status):
            out[t["id"]] = (status, t)
    return out


def ids_in(status: str) -> set:
    return {p.stem for p in (TASKS_DIR / status).glob("*.json")}


def move(task_id: str, src: str, dst: str, **updates) -> dict:
    # Update before renaming, so the file is never rewritten after another process can see it in dst.
    task = read(path(src, task_id))
    if updates:
        task.update(updates)
        write(src, task)
    os.rename(path(src, task_id), path(dst, task_id))
    return task


def is_ready(task: dict, done: set) -> bool:
    return all(dep in done for dep in task.get("depends_on", []))


def claim(agent_id: str, only: set = frozenset()) -> Optional[dict]:
    """Claim the first pending task whose dependencies are all done (restricted to `only` if given)."""
    done = ids_in("done")
    for task in list_tasks("pending"):
        if (only and task["id"] not in only) or not is_ready(task, done):
            continue
        try:
            os.rename(path("pending", task["id"]), path("claimed", task["id"]))
        except FileNotFoundError:
            continue  # another worker won the race
        task["agent"] = agent_id
        task["attempts"] = task.get("attempts", 0) + 1
        task.pop("error", None)
        write("claimed", task)
        return task
    return None


PROPOSAL_FIELDS = ("title", "role", "prompt", "scope")


def propose(source: dict, proposals: list) -> tuple:
    """Store an agent's proposals as <source id>.<n> in proposed/. Returns (ids, errors)."""
    ids, errors = [], []
    prefix = f"{source['id']}."
    taken = [int(tid[len(prefix):]) for tid in all_tasks() if tid.startswith(prefix) and tid[len(prefix):].isdigit()]
    start = max(taken, default=0) + 1
    for n, p in enumerate(proposals, start=start):
        missing = [f for f in PROPOSAL_FIELDS if not p.get(f)] if isinstance(p, dict) else list(PROPOSAL_FIELDS)
        if missing:
            errors.append(f"proposal {n}: missing {missing}")
            continue
        task = {k: p[k] for k in PROPOSAL_FIELDS}
        task.update(
            id=f"{source['id']}.{n}",
            depends_on=list(p.get("depends_on", [])),
            proposed_by=source["id"],
            severity=p.get("severity"),
            rationale=p.get("rationale"),
            attempts=0,
        )
        write("proposed", task)
        ids.append(task["id"])
    return ids, errors


def add(task: dict) -> None:
    task.setdefault("depends_on", [])
    task.setdefault("scope", [])
    task.setdefault("attempts", 0)
    write("pending", task)
