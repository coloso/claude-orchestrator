"""File-based task queue.

Each task is a JSON file; its status is the directory it lives in:

    .orch/tasks/pending/  ->  claimed/  ->  passed/  ->  done/
                                  \\-> failed/ (requeued until max_attempts)

Claiming is an atomic os.rename, so two workers can never grab the same task.
Agents can propose follow-up tasks; they wait in proposed/ until a human approves them,
unless config "auto_decide" settles them by severity (approve, or park in rejected/).
"""
from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Optional

from common import NON_WORKER_ROLES, ROLES_DIR, STATUSES, TASKS_DIR, emit


def path(status: str, task_id: str) -> Path:
    return TASKS_DIR / status / f"{task_id}.json"


def read(p: Path) -> dict:
    return json.loads(p.read_text())


def write(status: str, task: dict) -> None:
    # Write a new file and swap it in: truncating in place would let a reader that opened the
    # file just before a rename (e.g. a racing claim) see it empty.
    target = path(status, task["id"])
    tmp = target.with_suffix(".tmp")  # not *.json, so list_tasks never picks it up
    tmp.write_text(json.dumps(task, indent=2))
    os.replace(tmp, target)


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
    """Store an agent's proposals as <source id>.<n> in proposed/. Returns (ids, errors).

    depends_on entries may be existing task IDs or titles of other proposals in the same batch;
    titles are rewritten to the IDs assigned here. Unknown entries are dropped and reported."""
    ids, errors = [], []
    prefix = f"{source['id']}."
    existing = set(all_tasks())
    taken = [int(tid[len(prefix):]) for tid in existing if tid.startswith(prefix) and tid[len(prefix):].isdigit()]
    start = max(taken, default=0) + 1
    valid = []
    for n, p in enumerate(proposals, start=start):
        missing = [f for f in PROPOSAL_FIELDS if not p.get(f)] if isinstance(p, dict) else list(PROPOSAL_FIELDS)
        if missing:
            errors.append(f"proposal {n}: missing {missing}")
            continue
        valid.append((f"{source['id']}.{n}", p))
    batch_ids = {tid for tid, _ in valid}
    by_title = {str(p["title"]).strip(): tid for tid, p in valid}
    for tid, p in valid:
        deps = []
        raw = p.get("depends_on") or []
        for dep in [raw] if isinstance(raw, str) else raw:
            dep = str(dep).strip()
            resolved = dep if dep in existing or dep in batch_ids else by_title.get(dep)
            if resolved and resolved != tid:
                deps.append(resolved)
            else:
                errors.append(f"proposal {tid}: dropped dependency {dep!r} (not a task ID or a title in this batch)")
        task = {k: p[k] for k in PROPOSAL_FIELDS}
        task.update(
            id=tid,
            plan=source.get("plan"),
            model=p.get("model"),
            effort=p.get("effort"),
            depends_on=deps,
            proposed_by=source["id"],
            severity=p.get("severity"),
            rationale=p.get("rationale"),
            attempts=0,
        )
        write("proposed", task)
        ids.append(task["id"])
    return ids, errors


def approval_problem(task: dict, known: dict, config: dict) -> Optional[str]:
    """Why a proposal can't be queued as it is, or None."""
    models = config["models"]
    missing = [d for d in task.get("depends_on", []) if d not in known]
    if (missing or task["role"] in NON_WORKER_ROLES or not (ROLES_DIR / f"{task['role']}.md").exists()
            or (task.get("model") and task["model"] not in models["workers"])
            or (task.get("effort") and task["effort"] not in models["efforts"])):
        return (f"role '{task['role']}', model '{task.get('model')}', effort '{task.get('effort')}', "
                f"unknown deps {missing}")
    return None


def auto_decide(ids: list, config: dict) -> None:
    """Settle new proposals by severity, as config "auto_decide" says ({"medium": "approve", "low": "park"}).

    Anything else (critical, high, no severity) waits for a human. A parked proposal goes to rejected/
    with a `parked` note and can still be approved later. An approval is held back for the human when
    the proposal depends on one that is still waiting or was parked, so it never blocks the queue."""
    rules = config.get("auto_decide") or {}
    severity = {tid: (read(path("proposed", tid)).get("severity") or "").lower() for tid in ids}
    for tid in ids:
        if rules.get(severity[tid]) == "park":
            move(tid, "proposed", "rejected", parked=f"auto: {severity[tid]} severity")
            emit("task_parked", task=tid, detail=f"{severity[tid]} severity, parked automatically")
    todo = [tid for tid in ids if rules.get(severity[tid]) == "approve"]
    progress = True
    while todo and progress:  # repeat, so a proposal that depends on another one in the batch follows it
        progress = False
        known = all_tasks()
        for tid in list(todo):
            task = known[tid][1]
            if any(known.get(d, (None,))[0] == "proposed" and d in todo for d in task.get("depends_on", [])):
                continue
            todo.remove(tid)
            blocked = [d for d in task.get("depends_on", []) if known.get(d, (None,))[0] in ("proposed", "rejected")]
            problem = approval_problem(task, known, config) or (f"depends on {blocked}, still waiting for you or parked" if blocked else None)
            if problem:
                emit("task_held", task=tid, detail=f"{severity[tid]} severity, not approved automatically: {problem}")
                continue
            move(tid, "proposed", "pending", auto_approved=f"auto: {severity[tid]} severity")
            emit("task_approved", task=tid, detail=f"{severity[tid]} severity, approved automatically")
            progress = True
    for tid in todo:  # their dependencies in the batch were held back
        emit("task_held", task=tid, detail=f"{severity[tid]} severity, not approved automatically: depends on a held proposal")


def add(task: dict) -> None:
    task.setdefault("depends_on", [])
    task.setdefault("scope", [])
    task.setdefault("attempts", 0)
    write("pending", task)
