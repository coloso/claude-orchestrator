"""Shared paths, config, event log and shell helpers."""
from __future__ import annotations

import json
import os
import subprocess
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TOOLING = ROOT / "orchestration"
ORCH = ROOT / ".orch"
TASKS_DIR = ORCH / "tasks"
LOGS_DIR = ORCH / "logs"
AGENTS_DIR = ORCH / "agents"
MCP_DIR = ORCH / "mcp"
EVENTS_FILE = ORCH / "events.jsonl"
STATE_FILE = ORCH / "state.json"
STOP_FILE = ORCH / "STOP"
WORKTREES = ROOT / ".worktrees"
ROLES_DIR = TOOLING / "roles"
# Roles the orchestrator runs itself; they never get tasks from a plan or a proposal.
NON_WORKER_ROLES = ("planner", "lead")

# Human-written input, kept out of the tooling: stories, and the plans the planner makes from them.
BACKLOG = ROOT / "backlog"
STORY_FILES_DIR = BACKLOG / "stories"
PLANS_DIR = BACKLOG / "plans"

# "proposed" tasks come from agents and wait for `orchestrator.py approve`; "rejected" is terminal.
STATUSES = ["proposed", "pending", "claimed", "passed", "done", "failed", "rejected"]

# Agents write follow-up task proposals here, at their worktree root (gitignored, never committed).
PROPOSALS_FILE = ".orch-proposals.json"


def load_config() -> dict:
    config = json.loads((TOOLING / "config.json").read_text())
    models = config["models"]
    single = [models["planner"], *(models[k] for k in ("reviewer", "lead") if k in models)]
    ids = [m["id"] for m in single] + [w["id"] for w in models["workers"].values()]
    banned = [i for i in ids for word in models.get("forbidden", []) if word in i.lower()]
    if banned:
        raise SystemExit(f"config.json: forbidden model(s) {banned} (models.forbidden = {models['forbidden']})")
    if models["default_worker"] not in models["workers"]:
        raise SystemExit(f"config.json: default_worker '{models['default_worker']}' is not in models.workers")
    bad = [e for e in (m.get("effort") for m in [*single, *models["workers"].values()])
           if e and e not in models["efforts"]]
    if bad:
        raise SystemExit(f"config.json: effort(s) {bad} not in models.efforts {models['efforts']}")
    return config


def worker_model(task: dict, config: dict) -> tuple:
    """(alias, {"id", "effort"}) for a task.

    First attempt: the task's model and effort (or the model's default effort).
    Retries escalate: to the next model tier (escalate_on_retry), or, already on the
    top tier, to the next effort level (e.g. opus high -> xhigh).
    """
    models = config["models"]
    alias = task.get("model") or models["default_worker"]
    if alias not in models["workers"]:
        raise ValueError(f"model '{alias}' is not an allowed worker model {sorted(models['workers'])}")
    effort = task.get("effort") or models["workers"][alias].get("effort")
    if effort and effort not in models["efforts"]:
        raise ValueError(f"effort '{effort}' is not one of {models['efforts']}")
    if task.get("attempts", 1) > 1:
        higher = models.get("escalate_on_retry", {}).get(alias)
        levels = models["efforts"]
        if higher:
            # Never retry below the stronger model's default: a task that failed at low effort needs more, not the same.
            alias = higher
            default = models["workers"][alias].get("effort") or "high"
            effort = max(effort or default, default, key=levels.index)
        else:
            current = levels.index(effort) if effort else levels.index("high")
            effort = levels[min(current + 1, len(levels) - 1)]
    return alias, {"id": models["workers"][alias]["id"], "effort": effort}


def model_args(settings: dict) -> list:
    """claude CLI arguments for a models.* entry."""
    return ["--model", settings["id"], *(["--effort", settings["effort"]] if settings.get("effort") else [])]


def ensure_dirs() -> None:
    for d in [LOGS_DIR, AGENTS_DIR, MCP_DIR, WORKTREES, *(TASKS_DIR / s for s in STATUSES)]:
        d.mkdir(parents=True, exist_ok=True)


def emit(kind: str, **fields) -> None:
    """Append one event to the shared event log (read by the dashboard)."""
    event = {"ts": time.time(), "kind": kind, **fields}
    with EVENTS_FILE.open("a") as f:
        f.write(json.dumps(event) + "\n")


def tool_env() -> dict:
    """Environment for agents and gates."""
    env = dict(os.environ)
    env.setdefault("MCP_TIMEOUT", "60000")  # first start of an npx-based MCP server downloads it
    return env


def sh(cmd, cwd: Path = ROOT, check: bool = True, **kw) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd, cwd=cwd, check=check, text=True, capture_output=True,
        shell=isinstance(cmd, str), **kw,
    )


def git(*args: str, cwd: Path = ROOT, check: bool = True) -> subprocess.CompletedProcess:
    return sh(["git", *args], cwd=cwd, check=check)
