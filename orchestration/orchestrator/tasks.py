"""File-based task queue.

Each task is a JSON file; its status is the directory it lives in:

    .orch/tasks/pending/  ->  claimed/  ->  passed/  ->  done/
                                  \\-> failed/ (requeued until max_attempts)

Claiming is an atomic os.rename, so two workers can never grab the same task.
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


def claim(agent_id: str) -> Optional[dict]:
    """Claim the first pending task whose dependencies are all done."""
    done = ids_in("done")
    for task in list_tasks("pending"):
        if not is_ready(task, done):
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


def add(task: dict) -> None:
    task.setdefault("depends_on", [])
    task.setdefault("scope", [])
    task.setdefault("attempts", 0)
    write("pending", task)
