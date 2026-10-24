"""Meta-agent: plans tasks, runs parallel workers, merges passing branches.

    python3 orchestration/orchestrator/orchestrator.py plan "Build a todo app"  # Claude planner -> tasks
    python3 orchestration/orchestrator/orchestrator.py load orchestration/plans/todo-tasks.json
    python3 orchestration/orchestrator/orchestrator.py run [--workers 4] [--fake]
    python3 orchestration/orchestrator/orchestrator.py status
    python3 orchestration/orchestrator/orchestrator.py reset
"""
from __future__ import annotations

import argparse
import functools
import json
import re
import shutil
import subprocess
import sys
import threading
import time
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import tasks
import worktrees
from common import (AGENTS_DIR, EVENTS_FILE, ORCH, ROLES_DIR, ROOT, STATE_FILE, STATUSES,
                    STOP_FILE, WORKTREES, emit, ensure_dirs, git, load_config, sh)


# ---------------------------------------------------------------- planning

def cmd_plan(goal: str) -> None:
    config = load_config()
    ensure_dirs()
    roles = sorted(p.stem for p in ROLES_DIR.glob("*.md") if p.stem != "planner")
    prompt = f"Goal: {goal}\n\nAvailable worker roles: {', '.join(roles)}\n\nReturn the task list as JSON."
    print("Planning with Claude...")
    result = sh([
        "claude", "-p", prompt,
        "--append-system-prompt", (ROLES_DIR / "planner.md").read_text(),
        "--model", config["planner"]["model"],
        "--allowedTools", *config["planner"]["allowed_tools"],
        "--output-format", "json",
    ], check=False, stdin=subprocess.DEVNULL)
    if result.returncode != 0:
        sys.exit(f"planner failed:\n{result.stdout}\n{result.stderr}")
    text = json.loads(result.stdout)["result"]
    match = re.search(r"\[.*\]", text, re.S)
    if not match:
        sys.exit(f"planner returned no JSON array:\n{text}")
    plan = json.loads(match.group(0))
    (ORCH / "plan.json").write_text(json.dumps(plan, indent=2))
    load_plan(plan)


def cmd_load(file: str) -> None:
    ensure_dirs()
    load_plan(json.loads((ROOT / file).read_text()))


def load_plan(plan: list) -> None:
    ids = {t["id"] for t in plan}
    existing = set(tasks.all_tasks())
    for t in plan:
        missing = [d for d in t.get("depends_on", []) if d not in ids | existing]
        if missing:
            sys.exit(f"{t['id']} depends on unknown task(s) {missing}")
        if not (ROLES_DIR / f"{t['role']}.md").exists():
            sys.exit(f"{t['id']} has unknown role '{t['role']}'")
    for t in plan:
        tasks.add(t)
        emit("task_added", task=t["id"], detail=t["title"])
    print(f"Queued {len(plan)} task(s):")
    for t in plan:
        deps = f"  (after {', '.join(t['depends_on'])})" if t.get("depends_on") else ""
        print(f"  {t['id']}  [{t['role']}]  {t['title']}{deps}")


# ---------------------------------------------------------------- running

def cmd_run(n_workers: int, fake: bool) -> None:
    config = load_config()
    ensure_dirs()
    config["base_branch"] = preflight()
    STOP_FILE.unlink(missing_ok=True)

    # Recover tasks left claimed by a previous interrupted run.
    for t in tasks.list_tasks("claimed"):
        tasks.move(t["id"], "claimed", "pending")

    start_dashboard(config["dashboard_port"])
    print(f"Dashboard: http://localhost:{config['dashboard_port']}/orchestration/dashboard/")

    workers = []
    for i in range(1, n_workers + 1):
        cmd = [sys.executable, str(Path(__file__).with_name("worker.py")),
               "--id", f"agent-{i}", "--base", config["base_branch"]]
        workers.append(subprocess.Popen(cmd + (["--fake"] if fake else []), cwd=ROOT))
    emit("run_start", workers=n_workers, fake=fake)
    print(f"Started {n_workers} worker(s) on '{config['base_branch']}'"
          f"{' in --fake mode' if fake else ''}. Ctrl-C to stop.")

    idle_ticks = 0
    try:
        while True:
            merge_passed(config)
            requeue_failed(config)
            write_state()
            idle_ticks = idle_ticks + 1 if nothing_left_to_do() else 0
            if idle_ticks >= 2:
                break
            time.sleep(config["poll_interval_s"])
    except KeyboardInterrupt:
        print("\nStopping...")
    finally:
        STOP_FILE.touch()
        for w in workers:
            try:
                w.wait(timeout=10)
            except subprocess.TimeoutExpired:
                w.terminate()
        emit("run_stop")
        write_state()
    print_status()


def preflight() -> str:
    """Returns the current branch: worktrees start from it and finished tasks merge into it."""
    if git("status", "--porcelain").stdout.strip():
        sys.exit("Working tree has uncommitted changes; commit or stash them so branches can be merged.")
    branch = git("rev-parse", "--abbrev-ref", "HEAD").stdout.strip()
    if branch.startswith("agent/") or branch == "HEAD":
        sys.exit(f"Check out a normal branch first (currently on '{branch}').")
    return branch


def merge_passed(config: dict) -> None:
    """Merge passing branches into the base branch one at a time."""
    for t in tasks.list_tasks("passed"):
        tid = t["id"]
        branch = worktrees.branch_for(tid)
        result = git("merge", "--no-ff", "-m", f"Merge {tid}: {t['title']}", branch, check=False)
        if result.returncode != 0:
            git("merge", "--abort", check=False)
            tasks.move(tid, "passed", "failed", error=f"merge conflict:\n{result.stdout[-800:]}")
            emit("task_failed", task=tid, agent=t.get("agent"), error="merge conflict")
            continue
        tasks.move(tid, "passed", "done")
        worktrees.remove(tid)
        emit("task_done", task=tid, agent=t.get("agent"), detail=f"merged {branch}")
        print(f"  merged {tid}: {t['title']}")


def requeue_failed(config: dict) -> None:
    for t in tasks.list_tasks("failed"):
        if t.get("attempts", 0) < config["max_attempts"]:
            tasks.move(t["id"], "failed", "pending")
            emit("task_requeued", task=t["id"], attempt=t["attempts"] + 1)


def nothing_left_to_do() -> bool:
    # Read in queue order (pending before claimed before passed) so a task moving
    # between directories during the scan is always seen in one of them.
    pending = tasks.list_tasks("pending")
    if tasks.ids_in("claimed") or tasks.ids_in("passed"):
        return False
    done = tasks.ids_in("done")
    return not any(tasks.is_ready(t, done) for t in pending)


# ---------------------------------------------------------------- state / dashboard

def write_state() -> None:
    events = []
    if EVENTS_FILE.exists():
        events = [json.loads(line) for line in EVENTS_FILE.read_text().splitlines() if line]
    agents = [json.loads(p.read_text()) for p in sorted(AGENTS_DIR.glob("*.json"))]
    task_rows = []
    for tid, (status, t) in sorted(tasks.all_tasks().items()):
        task_rows.append({**{k: t.get(k) for k in
                             ("id", "title", "role", "depends_on", "scope", "agent", "attempts", "error")},
                          "status": status})
    cost = sum(e.get("cost_usd") or 0 for e in events if e["kind"] == "agent_result")
    STATE_FILE.write_text(json.dumps({
        "updated": time.time(),
        "running": not STOP_FILE.exists(),
        "cost_usd": round(cost, 4),
        "tasks": task_rows,
        "agents": agents,
        "events": events[-100:],
    }))


def start_dashboard(port: int) -> None:
    class Quiet(SimpleHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def end_headers(self):
            self.send_header("Cache-Control", "no-store")
            super().end_headers()

    handler = functools.partial(Quiet, directory=str(ROOT))
    try:
        server = ThreadingHTTPServer(("127.0.0.1", port), handler)
    except OSError as e:
        print(f"(dashboard not started: {e})")
        return
    threading.Thread(target=server.serve_forever, daemon=True).start()


# ---------------------------------------------------------------- status / reset

def print_status() -> None:
    rows = tasks.all_tasks()
    if not rows:
        print("No tasks. Use `plan` or `load` first.")
        return
    print(f"\n{'ID':<7}{'STATUS':<9}{'ROLE':<10}{'AGENT':<9}TITLE")
    for tid, (status, t) in sorted(rows.items()):
        print(f"{tid:<7}{status:<9}{t['role']:<10}{t.get('agent') or '-':<9}{t['title']}")
        if status == "failed" and t.get("error"):
            print(f"{'':<16}error: {t['error'].splitlines()[0][:100]}")
    counts = {s: sum(1 for st, _ in rows.values() if st == s) for s in STATUSES}
    print("\n" + "  ".join(f"{s}={n}" for s, n in counts.items() if n))


def cmd_reset() -> None:
    STOP_FILE.parent.mkdir(parents=True, exist_ok=True)
    STOP_FILE.touch()
    for line in git("branch", "--list", "agent/*").stdout.splitlines():
        worktrees.remove(line.strip().lstrip("*+ ").removeprefix("agent/"))
    shutil.rmtree(WORKTREES, ignore_errors=True)
    git("worktree", "prune", check=False)
    shutil.rmtree(ORCH, ignore_errors=True)
    print("Removed .orch/, .worktrees/ and agent/* branches. Merged commits on main are kept.")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("plan").add_argument("goal")
    sub.add_parser("load").add_argument("file")
    run = sub.add_parser("run")
    run.add_argument("--workers", type=int, default=load_config()["max_workers"])
    run.add_argument("--fake", action="store_true", help="dry run: workers write placeholders instead of calling Claude")
    sub.add_parser("status")
    sub.add_parser("reset")
    args = ap.parse_args()

    if args.cmd == "plan":
        cmd_plan(args.goal)
    elif args.cmd == "load":
        cmd_load(args.file)
    elif args.cmd == "run":
        cmd_run(args.workers, args.fake)
    elif args.cmd == "status":
        print_status()
    elif args.cmd == "reset":
        cmd_reset()


if __name__ == "__main__":
    main()
