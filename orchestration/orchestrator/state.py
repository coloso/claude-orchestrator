"""Dashboard state: everything the dashboard shows, computed into .orch/state.json.

Besides raw tasks and events, it derives what a person would otherwise have to ask:
which stage a task is in, how long it took and what it cost, what the planner is doing,
and what to do next (with the exact command).
"""
from __future__ import annotations

import json
import os
import socket
import time
from pathlib import Path

import tasks
from common import AGENTS_DIR, EVENTS_FILE, LOGS_DIR, ORCH, ROOT, STATE_FILE, TOOLING, git

RUN_FILE = ORCH / "run.json"


def record_run(**info) -> None:
    """Called by `run` at start (pid, settings) and end (finished)."""
    current = json.loads(RUN_FILE.read_text()) if RUN_FILE.exists() else {}
    if "pid" in info:
        current = {}
    current.update(info)
    RUN_FILE.write_text(json.dumps(current))


def write_state() -> None:
    events = _read_jsonl(EVENTS_FILE)
    run = _run_info()
    queue = tasks.all_tasks()
    rows = [_task_row(tid, status, t, events) for tid, (status, t) in sorted(queue.items())]
    _assign_stages(rows)
    agents = []
    for p in sorted(AGENTS_DIR.glob("*.json")):
        a = json.loads(p.read_text())
        if not run["active"] and a.get("status") != "stopped":
            a["status"] = "stopped"
        agents.append(a)
    state = {
        "updated": time.time(),
        "run": run,
        "git": _git_info(),
        "mongo_up": _port_open(27017),
        "cost_usd": round(sum(e.get("cost_usd") or 0 for e in events if e["kind"] == "agent_result"), 4),
        "tasks": rows,
        "agents": agents,
        "planner": _planner_info(),
        "plans": _plans_info(set(queue)),
        "events": events[-150:],
    }
    state["hints"] = _hints(state)
    tmp = STATE_FILE.with_suffix(".tmp")
    tmp.write_text(json.dumps(state))
    tmp.replace(STATE_FILE)  # atomic: the dashboard never reads a half-written file


# ---------------------------------------------------------------- pieces

def _read_jsonl(path: Path) -> list:
    if not path.exists():
        return []
    out = []
    for line in path.read_text().splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def _run_info() -> dict:
    info = json.loads(RUN_FILE.read_text()) if RUN_FILE.exists() else {}
    alive = False
    if info.get("pid") and not info.get("finished"):
        try:
            os.kill(info["pid"], 0)
            alive = True
        except OSError:
            pass
    return {**info, "active": alive}


def _git_info() -> dict:
    branch = git("rev-parse", "--abbrev-ref", "HEAD", check=False).stdout.strip()
    dirty = [l[3:] for l in git("status", "--porcelain", check=False).stdout.splitlines() if l.strip()]
    return {"branch": branch, "dirty": dirty}


def _port_open(port: int) -> bool:
    with socket.socket() as s:
        s.settimeout(0.2)
        return s.connect_ex(("127.0.0.1", port)) == 0


def _task_row(tid: str, status: str, t: dict, events: list) -> dict:
    mine = [e for e in events if e.get("task") == tid]
    claimed = [e["ts"] for e in mine if e["kind"] == "task_claimed"]
    done = [e["ts"] for e in mine if e["kind"] == "task_done"]
    results = [e for e in mine if e["kind"] == "agent_result"]
    tools = [e for e in mine if e["kind"] == "tool"]
    models = [e.get("detail") for e in mine if e["kind"] == "model"]
    started = claimed[-1] if claimed else None
    finished = done[-1] if done else None
    return {
        **{k: t.get(k) for k in ("id", "title", "role", "model", "effort", "depends_on", "scope", "agent",
                                 "attempts", "error", "severity", "proposed_by", "rationale", "prompt")},
        "status": status,
        "started": started,
        "duration_s": round((finished or time.time()) - started) if started and status != "failed" else None,
        "cost_usd": round(sum(r.get("cost_usd") or 0 for r in results), 4),
        "turns": sum(r.get("turns") or 0 for r in results),
        "last_tool": tools[-1]["detail"] if tools and status == "claimed" else None,
        "ran_as": models[-1] if models else None,
        "logs": sorted(p.name for p in LOGS_DIR.glob(f"{tid}.attempt*.jsonl")),
        "failures": [e.get("error", "")[:2000] for e in mine if e["kind"] == "task_failed"],
    }


def _assign_stages(rows: list) -> None:
    by_id = {r["id"]: r for r in rows}

    def stage(r, seen=()):
        if r.get("_stage"):
            return r["_stage"]
        deps = [by_id[d] for d in r.get("depends_on") or [] if d in by_id and d not in seen]
        r["_stage"] = 1 + max((stage(d, seen + (r["id"],)) for d in deps), default=0)
        return r["_stage"]

    for r in rows:
        stage(r)
    for r in rows:
        r["stage"] = r.pop("_stage")


def _planner_info():
    logs = sorted(LOGS_DIR.glob("planner-*.jsonl"), key=lambda p: p.stat().st_mtime)
    if not logs:
        return None
    log = logs[-1]
    name = log.stem[len("planner-"):]
    msgs = _read_jsonl(log)
    tools, tokens, result = [], 0, None
    for m in msgs:
        if m.get("type") == "assistant":
            for b in m.get("message", {}).get("content", []):
                if b.get("type") == "tool_use":
                    inp = b.get("input", {})
                    detail = inp.get("file_path") or inp.get("pattern") or inp.get("command") or ""
                    tools.append(f"{b['name']} {str(detail).replace(str(ROOT) + '/', '')}"[:120])
        elif m.get("subtype") == "thinking_tokens":
            tokens = m.get("estimated_tokens", tokens)
        elif m.get("type") == "result":
            result = m
    started = log.stat().st_birthtime if hasattr(log.stat(), "st_birthtime") else log.stat().st_ctime
    plan_file = TOOLING / "plans" / f"{name}.json"
    return {
        "name": name,
        "log": log.name,
        "started": started,
        "updated": log.stat().st_mtime,
        "finished": result is not None,
        "error": (result or {}).get("result") if (result or {}).get("is_error") else None,
        "cost_usd": (result or {}).get("total_cost_usd"),
        "thinking_tokens": tokens,
        "tools": tools[-8:],
        "tool_count": len(tools),
        "plan_file": plan_file.relative_to(ROOT).as_posix() if plan_file.exists() else None,
    }


def _plans_info(queued: set) -> list:
    out = []
    for p in sorted((TOOLING / "plans").glob("*.json")):
        try:
            ids = [t["id"] for t in json.loads(p.read_text())]
        except (json.JSONDecodeError, KeyError, TypeError):
            ids = []
        loaded = sum(1 for i in ids if i in queued)
        out.append({"file": p.relative_to(ROOT).as_posix(), "tasks": len(ids),
                    "loaded": "all" if ids and loaded == len(ids) else ("some" if loaded else "none")})
    return out


# ---------------------------------------------------------------- next steps

def _hints(s: dict) -> list:
    """Ordered list of {level: action|warn|info|ok, text, cmd?} — the dashboard's "what now?" panel."""
    hints = []
    add = lambda level, text, cmd=None: hints.append({"level": level, "text": text, "cmd": cmd})
    rows, run, planner = s["tasks"], s["run"], s["planner"]
    by = lambda st: [r for r in rows if r["status"] == st]
    done_ids = {r["id"] for r in by("done")}

    if planner and not planner["finished"] and time.time() - planner["updated"] < 120:
        add("info", f"Planner is working on {planner['name']}: {planner['tool_count']} files/commands read, "
                    f"~{planner['thinking_tokens'] // 1000}k thinking tokens. At high effort this takes minutes; "
                    "it is not hung while these numbers grow.")
    elif planner and planner["error"]:
        add("warn", f"The last planner run ({planner['name']}) failed: {planner['error'][:200]}",
            f"make plan STORY={planner['name']}")

    if run["active"]:
        working = [a for a in s["agents"] if a.get("status") in ("working", "gating")]
        add("info", f"Agents are working ({len(working)} busy). Nothing to do until they finish; "
                    "click a task to follow it.")
        if not run.get("mongo_test_uri"):
            add("warn", "This run was started without MONGO_TEST_URI: MongoDB tests are skipped and pass untested, "
                        "and agents have no MongoDB MCP server. Stop it (Ctrl-C) and restart in a terminal that has it.",
                "make mongo && export MONGO_TEST_URI=mongodb://localhost:27017 && make run")

    if not rows:
        unloaded = [p for p in s["plans"] if p["loaded"] == "none"]
        if unloaded:
            for p in unloaded:
                add("action", f"Plan {p['file']} ({p['tasks']} tasks) is not queued. Review it, commit it, then load it.",
                    f"make load PLAN={p['file']}")
        else:
            add("action", "Nothing planned yet. Write a story (copy stories/TEMPLATE.md), then plan it.",
                "make plan STORY=<story-name>")
        return hints

    proposed = by("proposed")
    if proposed:
        ids = " ".join(r["id"] for r in proposed)
        add("action", f"{len(proposed)} follow-up task(s) proposed by agents wait for your decision "
                      "(click them to read why).", f'make proposals   then: make approve IDS="{ids}"  or  make reject IDS="…"')

    for r in by("failed"):
        first = (r.get("error") or "").strip().splitlines()[0][:200] if r.get("error") else "unknown error"
        blocked = [x["id"] for x in by("pending") if r["id"] in (x.get("depends_on") or [])]
        add("action", f"{r['id']} failed after {r.get('attempts')} attempt(s): {first}"
                      + (f" — it blocks {', '.join(blocked)}." if blocked else "")
                      + " Click it for the full error and the agent's log; adjust its prompt/scope if needed, then retry.",
            f"make retry IDS={r['id']} && make run")

    pending = by("pending")
    ready = [r for r in pending if set(r.get("depends_on") or []) <= done_ids]
    if pending and not run["active"]:
        if s["git"]["dirty"]:
            add("warn", f"Uncommitted changes ({len(s['git']['dirty'])} files, e.g. {', '.join(s['git']['dirty'][:3])}). "
                        "`make run` needs a clean working tree.", 'git add -A && git commit -m "…"')
        if not s["mongo_up"]:
            add("warn", "MongoDB is not running on localhost:27017, so MongoDB tests would be skipped.", "make mongo")
        add("info", "Reminder: start `make run` in a terminal where MONGO_TEST_URI is exported, or MongoDB tests are skipped.",
            "export MONGO_TEST_URI=mongodb://localhost:27017")
        if ready:
            first_stage = min(r["stage"] for r in ready)
            stage_ids = ",".join(r["id"] for r in ready if r["stage"] == first_stage)
            if not done_ids:
                add("action", f"Ready to start. Recommended: run stage {first_stage} first and review it "
                              f"before the rest builds on it — or run everything.",
                    f"make run ONLY={stage_ids}      (or: make run)")
            else:
                add("action", f"{len(ready)} task(s) are ready to run.", "make run")
        elif not by("failed"):
            add("info", "Pending tasks wait for dependencies that are not done yet.")

    if rows and not pending and not proposed and not by("claimed") and not by("passed") and not by("failed"):
        add("ok", "Everything is merged. Verify the result on your branch.", "make check && make up")
    elif done_ids and not run["active"] and not ready and not proposed and not by("failed"):
        add("ok", "All runnable work is done. Check the merged result before continuing.", "make check")
    return hints
