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
EVENTS_FILE = ORCH / "events.jsonl"
STATE_FILE = ORCH / "state.json"
STOP_FILE = ORCH / "STOP"
WORKTREES = ROOT / ".worktrees"
ROLES_DIR = TOOLING / "roles"

# "proposed" tasks come from agents and wait for `orchestrator.py approve`; "rejected" is terminal.
STATUSES = ["proposed", "pending", "claimed", "passed", "done", "failed", "rejected"]

# Agents write follow-up task proposals here, at their worktree root (gitignored, never committed).
PROPOSALS_FILE = ".orch-proposals.json"


def load_config() -> dict:
    return json.loads((TOOLING / "config.json").read_text())


def ensure_dirs() -> None:
    for d in [LOGS_DIR, AGENTS_DIR, WORKTREES, *(TASKS_DIR / s for s in STATUSES)]:
        d.mkdir(parents=True, exist_ok=True)


def emit(kind: str, **fields) -> None:
    """Append one event to the shared event log (read by the dashboard)."""
    event = {"ts": time.time(), "kind": kind, **fields}
    with EVENTS_FILE.open("a") as f:
        f.write(json.dumps(event) + "\n")


def tool_env() -> dict:
    """Environment for agents and gates (hook for toolchain paths, e.g. a pinned Go or Node)."""
    return dict(os.environ)


def sh(cmd, cwd: Path = ROOT, check: bool = True, **kw) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd, cwd=cwd, check=check, text=True, capture_output=True,
        shell=isinstance(cmd, str), **kw,
    )


def git(*args: str, cwd: Path = ROOT, check: bool = True) -> subprocess.CompletedProcess:
    return sh(["git", *args], cwd=cwd, check=check)
