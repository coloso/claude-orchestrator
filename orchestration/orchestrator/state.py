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

import stories
import tasks
from common import AGENTS_DIR, EVENTS_FILE, LOGS_DIR, ORCH, PLANS_DIR, ROOT, STATE_FILE, git

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
        "stories": _stories_info(rows),
        "events": events[-150:],
    }
    state["hints"] = _hints(state)
    # Per-process temp file: `make dashboard` and `make run` both write the state, and a shared
    # temp file would be renamed away by one of them while the other still needs it.
    tmp = STATE_FILE.with_suffix(f".{os.getpid()}.tmp")
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
                                 "attempts", "error", "severity", "proposed_by", "rationale", "prompt",
                                 "parked", "auto_approved")},
        "plan": stories.plan_of(t),  # resolved, so the dashboard can group tasks by story branch
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
    plan_file = PLANS_DIR / f"{name}.json"
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


def _contains(target: str, ref: str) -> bool:
    return git("merge-base", "--is-ancestor", ref, target, check=False).returncode == 0


def _stories_info(rows: list) -> list:
    """One entry per plan: its story branch, progress, and (once complete) the final check."""
    plans = sorted({stories.plan_of(r) for r in rows} | {p.stem for p in stories.STORIES_DIR.glob("*.json")})
    mainline = stories.mainline()
    out = []
    for plan in plans:
        mine = [r for r in rows if stories.plan_of(r) == plan]
        rec = stories.info(plan)
        open_ = [r for r in mine if r["status"] in stories.ACTIVE]
        waiting = [r["id"] for r in mine if r["status"] == "proposed"]
        complete = bool(rec.get("completed_at")) and not open_
        local = [b for b in (stories.branch(plan), stories.pr_branch(plan)) if stories.branch_exists(b)]
        merged = [b for b in local if mainline and _contains(mainline, b)]
        # Shipped: the story (or its squashed pr/ commit) is in the mainline, or its branches were
        # deleted after it was complete, i.e. it was merged and cleaned up.
        shipped = complete and bool(mainline) and (not local or bool(merged))
        out.append({
            "plan": plan,
            "branch": stories.branch(plan),
            "base": rec.get("base"),
            "tasks": len(mine),
            "done": sum(1 for r in mine if r["status"] == "done"),
            "open": len(open_),
            "proposals": waiting,
            "complete": complete,
            "shipped": shipped,
            "mainline": mainline,
            # Local branches left over once shipped: merged ones can go with `git branch -d`; the
            # others were squashed into a merged pr/ commit and need -D.
            "leftover_merged": merged if shipped else [],
            "leftover_squashed": [b for b in local if b not in merged] if shipped else [],
            "integrating": stories.worktree_path(plan).exists(),
            "commits": rec.get("commits"),
            "files": rec.get("files"),
            "checked": rec.get("checked", True),
            "ok": rec.get("ok"),
            "checks": [{"gate": c["gate"], "ok": c["ok"], "output": c["output"][-1500:]} for c in rec.get("checks", [])],
            "review": stories.review_commands(plan) if rec.get("base") else None,
            "lead": rec.get("review") or None,
            "pr_branch": rec.get("pr_branch"),
            "pr_commit": rec.get("pr_commit"),
            "pr_title": rec.get("pr_title"),
            "merged_into": rec.get("merged_into"),
            "merge_skipped": rec.get("merge_skipped"),
        })
    return out


def _plans_info(queued: set) -> list:
    out = []
    for p in sorted(PLANS_DIR.glob("*.json")):
        try:
            ids = [t["id"] for t in json.loads(p.read_text())]
        except (json.JSONDecodeError, KeyError, TypeError):
            ids = []
        loaded = sum(1 for i in ids if i in queued)
        out.append({"file": p.relative_to(ROOT).as_posix(), "tasks": len(ids),
                    "loaded": "all" if ids and loaded == len(ids) else ("some" if loaded else "none")})
    return out


# ---------------------------------------------------------------- next steps

def _merged_commands(st: dict) -> str:
    """For a story fast-forwarded onto its base: push it, and open a PR unless the base is the mainline
    itself (then pushing is all there is)."""
    branch, local_main = st["merged_into"], (stories.mainline() or "origin/main").split("/", 1)[-1]
    log = f"git log --oneline -3 {branch}\n"
    return log + ("git push" if branch == local_main else stories.pr_command(st["plan"], branch))


def _hints(s: dict) -> list:
    """Ordered list of {level: action|warn|info|ok, text, cmd?} — the dashboard's "what now?" panel."""
    hints = []
    add = lambda level, text, cmd=None: hints.append({"level": level, "text": text, "cmd": cmd})
    rows, run, planner = s["tasks"], s["run"], s["planner"]
    by = lambda st: [r for r in rows if r["status"] == st]
    done_ids = {r["id"] for r in by("done")}

    shipped = [st for st in s["stories"] if st["shipped"]]
    shipped_plans = {st["plan"] for st in shipped}
    branch = s["git"]["branch"]
    if shipped:
        mainline = shipped[0]["mainline"]
        local_main = mainline.split("/", 1)[-1]
        based_here = [st for st in s["stories"] if st["base"] == branch]
        if based_here and branch != local_main and all(st["shipped"] for st in based_here) \
                and _contains(mainline, "HEAD"):
            dirty = (f" Commit or stash your {len(s['git']['dirty'])} uncommitted change(s) first."
                     if s["git"]["dirty"] else "")
            add("action", f"You are on {branch}, which is already merged into {mainline}. "
                          f"Switch back to {local_main} before you start the next story.{dirty}",
                f"git switch {local_main} && git pull")

    for st in s["stories"]:
        if st["complete"] and not st["shipped"]:
            checks = ("checks skipped (dry run)" if not st["checked"]
                      else "all checks passed" if st["ok"] else "checks FAILED (see the story card)")
            waiting = (f" {len(st['proposals'])} proposal(s) still wait for your decision; review the branch first, "
                       "then approve or reject them (approving reopens the branch)." if st["proposals"] else "")
            lead = st["lead"] or {}
            verdict = {"approve": " The lead engineer approved it.",
                       "changes_requested": " The lead engineer requested changes (see the story card).",
                       "error": " The lead review failed (see the story card)."}.get(lead.get("verdict"), "")
            where = (f"merged onto {st['merged_into']} as one commit" if st["merged_into"]
                     else f"squashed into one commit on {st['pr_branch']}" if st["pr_commit"]
                     else f"on branch {st['branch']}")
            todo = ("Push it and open a pull request when you're happy with it." if st["merged_into"]
                    else f"It was not merged automatically ({st['merge_skipped']}). Review it and push it yourself."
                    if st["merge_skipped"] else "Review it and push it yourself.")
            good = (st["ok"] or not st["checked"]) and lead.get("verdict") in (None, "approve")
            add("ok" if good else "warn",
                f"Story {st['plan']} is ready, {where}: {st['commits']} task commits, "
                f"{st['files']} files, {checks}.{verdict} {todo}{waiting}",
                _merged_commands(st) if st["merged_into"] else st["review"])
            hints[-1]["plan"] = st["plan"]

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

    unloaded = [p for p in s["plans"] if p["loaded"] == "none"]
    if not run["active"]:
        for p in unloaded:
            add("action", f"Plan {p['file']} ({p['tasks']} tasks) is not queued. Review it, commit it, then load it.",
                f"make load PLAN={p['file']}")
    if not rows:
        if not unloaded:
            add("action", "Nothing planned yet. Write a story (copy backlog/stories/TEMPLATE.md), then plan it.",
                "make plan STORY=<story-name>")
        return hints

    proposed = by("proposed")
    if proposed:
        add("action", f"{len(proposed)} follow-up task(s) proposed by agents wait for your decision. "
                      "Go through them one by one in the terminal (approve, reject, or chat about each).",
            "make review")

    parked_all = [r for r in by("rejected") if r.get("parked")]
    parked = [r["id"] for r in parked_all if r["plan"] not in shipped_plans]
    parked_shipped = [r["id"] for r in parked_all if r["plan"] in shipped_plans]
    if parked:
        add("info", f"{len(parked)} low-severity proposal(s) were parked automatically: they are in the reports "
                    "but don't run. Approve one if you want it after all.",
            f"make approve IDS=\"{' '.join(parked)}\"")

    for r in by("failed"):
        first = (r.get("error") or "").strip().splitlines()[0][:200] if r.get("error") else "unknown error"
        blocked = [x["id"] for x in by("pending") if r["id"] in (x.get("depends_on") or [])]
        add("action", f"{r['id']} failed after {r.get('attempts')} attempt(s): {first}"
                      + (f" — it blocks {', '.join(blocked)}." if blocked else "")
                      + " Click it for the full error and the agent's log; adjust its prompt/scope if needed, then retry.",
            f"make retry IDS={r['id']}")
        hints[-1]["plan"] = r["plan"]

    pending = by("pending")
    ready = [r for r in pending if set(r.get("depends_on") or []) <= done_ids]
    if pending and not run["active"]:
        if s["git"]["branch"].startswith("story/"):
            add("warn", f"You are on {s['git']['branch']}. Switch back to your normal branch before `make run`.",
                "git switch main")
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
                plans = sorted({stories.plan_of(r) for r in ready})
                review = "\n".join(f"git log --oneline {stories.info(p).get('base', 'main')}..{stories.branch(p)}"
                                   f"   # review so far; or: make -C .worktrees/_story-{p} check" for p in plans)
                add("action", f"{len(ready)} task(s) are ready to run. Optionally review the work merged so far first.",
                    f"{review}\nmake run")
        elif not by("failed"):
            add("info", "Pending tasks wait for dependencies that are not done yet.")

    unfinished = [st for st in s["stories"] if not st["complete"] and st["open"] == 0 and st["integrating"]]
    for st in unfinished:
        add("action", f"All tasks of {st['plan']} are merged into {st['branch']}, but the final check hasn't run yet. "
                      "It runs when `make run` finishes; start it once more to finalize.", "make run")
        hints[-1]["plan"] = st["plan"]

    idle = not run["active"] and not any(r["status"] in stories.ACTIVE + ("proposed",) for r in rows)
    if idle and not unloaded and s["stories"] and all(st["shipped"] for st in s["stories"]):
        add("action", f"All {len(s['stories'])} stories are shipped. Write the next one "
                      "(copy backlog/stories/TEMPLATE.md), then plan it.",
            "make plan STORY=backlog/stories/<next-story>.md")

    merged = [b for st in shipped for b in st["leftover_merged"]]
    squashed = [b for st in shipped for b in st["leftover_squashed"]]
    bases = sorted({st["base"] for st in shipped if st["base"] and st["base"] != branch
                    and st["base"] != shipped[0]["mainline"].split("/", 1)[-1]
                    and stories.branch_exists(st["base"]) and _contains(st["mainline"], st["base"])})
    if merged or squashed or bases:
        cmd = "\n".join(filter(None, [
            f"git branch -d {' '.join(bases + merged)}" if bases or merged else "",
            f"git branch -D {' '.join(squashed)}   # squashed into a pr/ commit that is merged" if squashed else ""]))
        add("info", f"{len(bases) + len(merged) + len(squashed)} local branch(es) of shipped stories are left over. "
                    "Delete them when you no longer need them (-d refuses anything that isn't merged).", cmd)
    if parked_shipped:
        add("info", f"{len(parked_shipped)} parked low-severity proposal(s) belong to stories already in "
                    f"{shipped[0]['mainline']}: {', '.join(parked_shipped)}. Approving one would reopen a merged "
                    "story; if one still matters, take it into a new story.")
    return hints
