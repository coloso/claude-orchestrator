"""Meta-agent: plans tasks, runs parallel workers, merges passing branches.

    python3 orchestration/orchestrator/orchestrator.py plan backlog/stories/S02-shopping-list.md  # planner -> plan file
    python3 orchestration/orchestrator/orchestrator.py load backlog/plans/S02-shopping-list.json
    python3 orchestration/orchestrator/orchestrator.py run [--workers 4] [--only S02-01,S02-02] [--fake]
    python3 orchestration/orchestrator/orchestrator.py dashboard              # live view, any time
    python3 orchestration/orchestrator/orchestrator.py status
    python3 orchestration/orchestrator/orchestrator.py proposals              # follow-ups agents proposed
    python3 orchestration/orchestrator/orchestrator.py review                 # decide them one by one, interactively
    python3 orchestration/orchestrator/orchestrator.py approve T-010.1 [...]  # queue them (then `run`)
    python3 orchestration/orchestrator/orchestrator.py reject T-010.2 [...]
    python3 orchestration/orchestrator/orchestrator.py retry T-004 [...]      # failed -> pending, attempts reset
    python3 orchestration/orchestrator/orchestrator.py reset
"""
from __future__ import annotations

import argparse
import functools
import json
import os
import re
import shutil
import subprocess
import sys
import textwrap
import threading
import time
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Optional

import mcp
import stories
import tasks
import worktrees
from common import (MCP_DIR, ORCH, PLANS_DIR, ROLES_DIR, ROOT, STATUSES, STOP_FILE, STORY_FILES_DIR, WORKTREES,
                    emit, ensure_dirs, git, load_config, model_args, tool_env)
from state import record_run, write_state


# ---------------------------------------------------------------- planning

def resolve_story(source: str) -> tuple:
    """(story text, name) from a story path, a name in backlog/stories/, or a one-line goal (must contain spaces)."""
    for candidate in (ROOT / source, STORY_FILES_DIR / source, STORY_FILES_DIR / f"{source}.md"):
        if candidate.is_file():
            return candidate.read_text(), candidate.stem
    if " " in source.strip():
        return source, re.sub(r"[^a-z0-9]+", "-", source.lower()).strip("-")[:40] or "plan"
    stories = sorted(p.relative_to(ROOT).as_posix() for p in STORY_FILES_DIR.glob("*.md") if p.name != "TEMPLATE.md")
    sys.exit(f"Story '{source}' not found (looked in ./ and backlog/stories/).\n"
             f"Create it from the template:  cp backlog/stories/TEMPLATE.md backlog/stories/{Path(source).stem}.md\n"
             f"Existing stories: {', '.join(stories) or 'none'}\n"
             'For a quick one-line goal instead, quote a sentence: make plan STORY="Add tags to todos"')


def cmd_plan(source: str, out: str, prefix: str) -> None:
    """Let the planner agent turn a story file (or a one-line goal) into a plan file for review."""
    config = load_config()
    story, name = resolve_story(source)
    prefix = prefix or story_prefix(name)
    out_path = ROOT / out if out else PLANS_DIR / f"{name}.json"
    if out_path.exists():
        sys.exit(f"{out_path.relative_to(ROOT)} already exists; pass --out or delete it.")

    roles = sorted(p.stem for p in ROLES_DIR.glob("*.md") if p.stem != "planner")
    prompt = (
        f"Story:\n\n{story}\n\n"
        f"Available worker roles: {', '.join(roles)}\n"
        f"Available worker models: {', '.join(config['models']['workers'])}\n"
        f"Available efforts: {', '.join(config['models']['efforts'])}\n"
        f"Use task IDs {prefix}-01, {prefix}-02, ... in order.\n\n"
        "Return the task list as JSON."
    )
    planner = config["models"]["planner"]
    ensure_dirs()
    servers, _ = mcp.servers_for("planner", config)
    cmd = [
        "claude", "-p", prompt,
        "--append-system-prompt", (ROLES_DIR / "planner.md").read_text(),
        *model_args(planner),
        "--allowedTools", *config["planner"]["allowed_tools"], *mcp.allowed_tools(servers),
        *mcp.claude_args(MCP_DIR / "planner.json", servers),
        "--output-format", "stream-json", "--verbose",
    ]
    log_path = ORCH / "logs" / f"planner-{name}.jsonl"
    timeout = config["planner"].get("timeout_s", 1200)
    print(f"Planning {name} with {planner['id']} (effort {planner.get('effort', 'default')}); "
          f"this can take a few minutes. Ctrl-C to abort. Log: {log_path.relative_to(ROOT)}")
    text = run_planner(cmd, log_path, timeout)
    match = re.search(r"\[.*\]", text, re.S)
    if not match:
        sys.exit(f"planner returned no JSON array:\n{text}")
    plan = json.loads(match.group(0))
    validate_plan(plan, config=config)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(plan, indent=2, ensure_ascii=False) + "\n")
    rel = out_path.relative_to(ROOT)
    print(f"Wrote {len(plan)} task(s) to {rel}:")
    print_plan(plan)
    print(f"\nReview or edit {rel}, then queue it:  make load PLAN={rel}")


def run_planner(cmd: list, log_path: Path, timeout: int) -> str:
    """Run the planner, echo its progress live, and return its final text."""
    start = time.time()
    proc = subprocess.Popen(cmd, cwd=ROOT, env=tool_env(), text=True, stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    timer = threading.Timer(timeout, proc.kill)
    timer.start()
    result, other = None, []
    last_heartbeat = start
    try:
        with log_path.open("w") as log:
            for line in proc.stdout:
                log.write(line)
                log.flush()
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    other.append(line)
                    continue
                elapsed = time.strftime("%M:%S", time.gmtime(time.time() - start))
                if msg.get("type") == "assistant":
                    for block in msg.get("message", {}).get("content", []):
                        if block.get("type") == "tool_use":
                            inp = block.get("input", {})
                            detail = (inp.get("file_path") or inp.get("pattern") or inp.get("path")
                                      or inp.get("command") or "")
                            print(f"  [{elapsed}] {block['name']} {str(detail).replace(str(ROOT) + '/', '')}"[:120],
                                  flush=True)
                            last_heartbeat = time.time()
                elif msg.get("type") == "system" and msg.get("subtype") == "thinking_tokens":
                    # Long stretches of thinking produce no tool calls; show a heartbeat so it doesn't look hung.
                    if time.time() - last_heartbeat >= 20:
                        print(f"  [{elapsed}] thinking… ~{msg.get('estimated_tokens', 0) / 1000:.0f}k tokens", flush=True)
                        last_heartbeat = time.time()
                elif msg.get("type") == "result":
                    result = msg
        proc.wait()
    except KeyboardInterrupt:
        proc.kill()
        sys.exit("\nPlanning aborted.")
    finally:
        timer.cancel()
    if result is None or result.get("is_error") or proc.returncode != 0:
        reason = "timed out" if time.time() - start >= timeout else f"exit code {proc.returncode}"
        detail = (result or {}).get("result") or "".join(other)[-1500:]
        sys.exit(f"Planner failed ({reason}). See {log_path.relative_to(ROOT)}\n{detail}")
    cost = result.get("total_cost_usd")
    print(f"  done in {time.strftime('%M:%S', time.gmtime(time.time() - start))}"
          + (f", ${cost:.2f}" if cost is not None else ""))
    return result.get("result", "")


def story_prefix(name: str) -> str:
    """S02-shopping-list -> S02; S-01-DB-Schema -> S01; shopping-list -> SHOPPING."""
    m = re.match(r"([A-Za-z]+)-?(\d+)", name)
    return (m.group(1) + m.group(2) if m else re.split(r"[^A-Za-z0-9]", name)[0][:8] or "T").upper()


def cmd_load(file: str) -> None:
    ensure_dirs()
    plan = json.loads((ROOT / file).read_text())
    for t in plan:  # tasks of one plan are merged into one story branch: story/<plan file name>
        t.setdefault("plan", Path(file).stem)
    load_plan(plan)


def validate_plan(plan: list, existing: set = frozenset(), config: dict = None) -> None:
    models = (config or load_config())["models"]
    workers = models["workers"]
    ids = [t["id"] for t in plan]
    dupes = sorted({i for i in ids if ids.count(i) > 1} | (set(ids) & existing))
    if dupes:
        sys.exit(f"task ID(s) already used: {dupes}")
    for t in plan:
        missing = [d for d in t.get("depends_on", []) if d not in set(ids) | existing]
        if missing:
            sys.exit(f"{t['id']} depends on unknown task(s) {missing}")
        if not (ROLES_DIR / f"{t['role']}.md").exists():
            sys.exit(f"{t['id']} has unknown role '{t['role']}'")
        if t.get("model") and t["model"] not in workers:
            sys.exit(f"{t['id']} asks for model '{t['model']}'; worker models are {sorted(workers)}")
        if t.get("effort") and t["effort"] not in models["efforts"]:
            sys.exit(f"{t['id']} asks for effort '{t['effort']}'; efforts are {models['efforts']}")


def print_plan(plan: list) -> None:
    for t in plan:
        deps = f"  (after {', '.join(t['depends_on'])})" if t.get("depends_on") else ""
        model = f"  <{'/'.join(filter(None, [t.get('model'), t.get('effort')]))}>" if t.get("model") or t.get("effort") else ""
        print(f"  {t['id']:<9}[{t['role']}]{model}  {t['title']}{deps}")


def load_plan(plan: list) -> None:
    config = load_config()
    validate_plan(plan, set(tasks.all_tasks()), config)
    models = config["models"]
    for t in plan:  # make the orchestrator's choice explicit in the queue: no implicit CLI defaults
        t["model"] = t.get("model") or models["default_worker"]
        t["effort"] = t.get("effort") or models["workers"][t["model"]]["effort"]
    for t in plan:
        tasks.add(t)
        emit("task_added", task=t["id"], detail=t["title"])
    print(f"Queued {len(plan)} task(s):")
    print_plan(plan)


# ---------------------------------------------------------------- running

def cmd_run(n_workers: int, fake: bool, only: list) -> None:
    config = load_config()
    ensure_dirs()
    config["base_branch"] = preflight()
    for plan in sorted(stories.open_plans()):
        stories.ensure(plan, config["base_branch"])
    if not fake:
        mcp.prepare(config)
    if only:
        unknown = [tid for tid in only if tid not in tasks.all_tasks()]
        if unknown:
            sys.exit(f"--only: unknown task(s) {unknown}")
    STOP_FILE.unlink(missing_ok=True)

    # Recover tasks left claimed by a previous interrupted run.
    for t in tasks.list_tasks("claimed"):
        tasks.move(t["id"], "claimed", "pending")

    record_run(pid=os.getpid(), started=time.time(), base_branch=config["base_branch"], workers=n_workers,
               fake=fake, only=only, mongo_test_uri=bool(os.environ.get("MONGO_TEST_URI")))
    start_dashboard(config["dashboard_port"])
    print(f"Dashboard: http://localhost:{config['dashboard_port']}/orchestration/dashboard/")

    workers = []
    for i in range(1, n_workers + 1):
        cmd = [sys.executable, str(Path(__file__).with_name("worker.py")), "--id", f"agent-{i}"]
        cmd += ["--fake"] if fake else []
        cmd += ["--only", ",".join(only)] if only else []
        workers.append(subprocess.Popen(cmd, cwd=ROOT))
    emit("run_start", workers=n_workers, fake=fake)
    branches = ", ".join(stories.branch(p) for p in sorted(stories.open_plans()))
    print(f"Started {n_workers} worker(s){' in --fake mode' if fake else ''}"
          f"{' for ' + ', '.join(only) if only else ''}. Merging into {branches or 'story branches'} "
          f"(based on '{config['base_branch']}'); your checkout is not touched. Ctrl-C to stop.")

    idle_ticks = 0
    try:
        while True:
            for plan in stories.open_plans():  # e.g. proposals approved while running
                stories.ensure(plan, config["base_branch"])
            merge_passed(config)
            requeue_failed(config)
            write_state()
            idle_ticks = idle_ticks + 1 if nothing_left_to_do(only) else 0
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
        completed = stories.completable_plans()
        for plan in completed:
            stories.finalize(plan, config, run_checks=not fake)
        record_run(finished=time.time())
        write_state()
    print_status()
    for plan in completed:
        stories.print_summary(plan)
    waiting = tasks.list_tasks("proposed")
    if waiting:
        print(f"\n{len(waiting)} proposed follow-up task(s) need your decision: "
              "`orchestrator.py proposals`, then `approve` / `reject`.")


def preflight() -> str:
    """Returns the current branch: new story branches start from its last commit."""
    branch = git("rev-parse", "--abbrev-ref", "HEAD").stdout.strip()
    if branch.startswith(("agent/", "story/")) or branch == "HEAD":
        sys.exit(f"Switch back to your normal branch first (currently on '{branch}'): "
                 "story branches are checked out in their own worktree while agents work on them.")
    if git("status", "--porcelain").stdout.strip():
        print("Note: you have uncommitted changes. That's fine; agents start from your last commit, "
              "so those changes are not part of the story branch.")
    return branch


def merge_passed(config: dict) -> None:
    """Merge passing branches into their story branch, one at a time."""
    for t in tasks.list_tasks("passed"):
        tid = t["id"]
        ok, output = stories.merge(t, config["base_branch"])
        if not ok:
            tasks.move(tid, "passed", "failed", error=f"merge conflict:\n{output}")
            emit("task_failed", task=tid, agent=t.get("agent"), error="merge conflict")
            continue
        # Log before moving: once the task is in done/, workers can claim its dependents.
        emit("task_done", task=tid, agent=t.get("agent"),
             detail=f"merged into {stories.branch(stories.plan_of(t))}")
        tasks.move(tid, "passed", "done")
        worktrees.remove(tid)
        print(f"  merged {tid}: {t['title']}")


def requeue_failed(config: dict) -> None:
    for t in tasks.list_tasks("failed"):
        if t.get("attempts", 0) < config["max_attempts"]:
            tasks.move(t["id"], "failed", "pending")
            emit("task_requeued", task=t["id"], attempt=t["attempts"] + 1)


def nothing_left_to_do(only: list = None) -> bool:
    # Read in queue order (pending before claimed before passed) so a task moving
    # between directories during the scan is always seen in one of them.
    pending = [t for t in tasks.list_tasks("pending") if not only or t["id"] in only]
    if tasks.ids_in("claimed") or tasks.ids_in("passed"):
        return False
    done = tasks.ids_in("done")
    return not any(tasks.is_ready(t, done) for t in pending)


# ---------------------------------------------------------------- state / dashboard

def cmd_dashboard() -> None:
    """Serve the dashboard and keep its state fresh, with or without a run in progress."""
    config = load_config()
    ensure_dirs()
    url = f"http://localhost:{config['dashboard_port']}/orchestration/dashboard/"
    serving = start_dashboard(config["dashboard_port"], quiet=True)
    print(f"Dashboard: {url}" + ("" if serving else "  (already served by `make run`; refreshing state only)"))
    print("Ctrl-C to stop.")
    try:
        while True:
            write_state()
            if not serving:
                serving = start_dashboard(config["dashboard_port"], quiet=True)
            time.sleep(1)
    except KeyboardInterrupt:
        pass


def start_dashboard(port: int, quiet: bool = False) -> bool:
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
        if not quiet:
            print(f"(dashboard already served elsewhere, e.g. by `make dashboard`: {e.strerror})")
        return False
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return True


# ---------------------------------------------------------------- proposals

def cmd_proposals() -> None:
    proposed = tasks.list_tasks("proposed")
    if not proposed:
        print("No proposals waiting.")
        return
    for t in proposed:
        print_proposal(t)
        print()


COLOR = sys.stdout.isatty() and not os.environ.get("NO_COLOR")
SEVERITY_COLORS = {"critical": "1;31", "high": "31", "medium": "33", "low": "32"}


def style(text: str, code: str) -> str:
    return f"\033[{code}m{text}\033[0m" if COLOR else text


def wrapped(text: str, indent: str = "  ") -> str:
    """Wrap each line to the terminal width, keeping list items' continuation lines under their text."""
    width = min(shutil.get_terminal_size((100, 20)).columns, 100)
    out = []
    for line in text.splitlines():
        if not line.strip():
            out.append("")
            continue
        item = re.match(r"(\s*(?:[-*•]|\d+[.)])\s+)", line)
        hang = " " * len(item.group(1)) if item else re.match(r"\s*", line).group(0)
        out.append(textwrap.fill(line, width, initial_indent=indent, subsequent_indent=indent + hang,
                                 break_long_words=False, break_on_hyphens=False))
    return "\n".join(out)


def print_proposal(t: dict, heading: str = "", full_task: bool = True) -> None:
    width = min(shutil.get_terminal_size((100, 20)).columns, 100)
    head = f"━━ {heading + ' · ' if heading else ''}{t['id']} "
    print(style(head + "━" * max(width - len(head), 0), "2"))
    print(style(t["title"], "1"))
    severity = t.get("severity")
    meta = [style(severity, SEVERITY_COLORS.get(severity, "0")) if severity else None,
            f"role {t['role']}",
            f"model {t.get('model') or 'default'}{'/' + t['effort'] if t.get('effort') else ''}",
            f"from {t['proposed_by']}",
            f"after {', '.join(t['depends_on'])}" if t.get("depends_on") else None]
    print(" · ".join(m for m in meta if m))
    if t.get("rationale"):
        print(f"\n{style('Why', '1;36')}\n{wrapped(t['rationale'])}")
    print(f"\n{style('Scope', '1;36')}")
    for s in t["scope"]:
        print(f"  • {s}")
    print(f"\n{style('Task', '1;36')}")
    print_task(t, full_task)


TASK_PREVIEW_LINES = 4


def print_task(t: dict, full: bool = True) -> None:
    """The task prompt; collapsed to its first paragraph (at most TASK_PREVIEW_LINES lines) unless full."""
    lines = wrapped(t["prompt"]).splitlines()
    if full:
        print("\n".join(lines))
        return
    shown = lines[:TASK_PREVIEW_LINES]
    if "" in shown:
        shown = shown[:shown.index("")]
    print("\n".join(shown))
    hidden = sum(1 for line in lines[len(shown):] if line.strip())
    if hidden:
        print(style(f"  … {hidden} more line{'s' if hidden != 1 else ''}, press t to show the full task", "2"))


CHOICES = style("[a]pprove  [r]eject  [s]kip  [t]ask  [c]hat about it  [q]uit", "1")


def cmd_review() -> None:
    """Go through the waiting proposals one by one and decide each in the terminal."""
    proposed = tasks.list_tasks("proposed")
    if not proposed:
        print("No proposals waiting.")
        return
    if not sys.stdin.isatty():
        sys.exit("review needs an interactive terminal; use `approve` / `reject` with IDs instead.")
    for i, t in enumerate(proposed, start=1):
        print()
        print_proposal(t, heading=f"Proposal {i}/{len(proposed)}", full_task=False)
        while True:
            try:
                answer = input(f"\n{CHOICES} › ").strip().lower()[:1]
            except (EOFError, KeyboardInterrupt):
                answer = "q"
            if answer == "t":
                print(f"\n{style('Task', '1;36')} {style(t['id'], '2')}")
                print_task(t)
                continue
            if answer == "c":
                t = chat_about_proposal(t, proposed)
                if t is None:
                    break
                continue
            if answer in ("a", "r", "s", "q"):
                break
        if t is None:
            continue
        if answer == "q":
            print("\nStopped; the remaining proposals keep waiting.")
            return
        if answer in ("a", "r"):
            cmd_decide([t["id"]], approve=answer == "a")


def chat_about_proposal(t: dict, batch: list) -> Optional[dict]:
    """Open an interactive Claude session about one proposal; returns it re-read from disk afterwards
    (it may have been edited in the chat), or None if it is no longer waiting."""
    config = load_config()
    reviewer = config["models"].get("reviewer", config["models"]["planner"])
    file = tasks.path("proposed", t["id"])
    others = "\n".join(f"- {o['id']}: {o['title']} (after {', '.join(o['depends_on']) or 'nothing'})"
                       for o in batch if o["id"] != t["id"])
    system = (
        "You are helping a human decide whether to approve, reject or change a follow-up task that an agent "
        "proposed. The human decides in the orchestrator's review prompt after this chat; do not approve, reject "
        "or queue anything yourself, and do not change code.\n\n"
        f"The proposal is stored in {file.relative_to(ROOT)}:\n\n{json.dumps(t, indent=2, ensure_ascii=False)}\n\n"
        f"Other proposals waiting:\n{others or '- none'}\n\n"
        "Read the code, docs and reports it refers to before answering. If the human wants the proposal changed "
        "(prompt, scope, model, effort, depends_on, ...), edit only that JSON file and keep it valid JSON with "
        "the same id. depends_on holds task IDs."
    )
    servers, _ = mcp.servers_for("planner", config)
    ensure_dirs()
    cmd = [
        "claude", f"Summarize proposal {t['id']} in a few lines: what it changes, why, its risks, and how it "
                  "relates to the other proposals. Then wait for my questions.",
        "--append-system-prompt", system,
        *model_args(reviewer),
        "--allowedTools", *config["planner"]["allowed_tools"], *mcp.allowed_tools(servers),
        *mcp.claude_args(MCP_DIR / "review.json", servers),
    ]
    print(f"\nStarting a chat about {t['id']} with {reviewer['id']}. Exit with /exit or Ctrl-D to get back here.\n")
    try:
        subprocess.run(cmd, cwd=ROOT, env=tool_env())
    except FileNotFoundError:
        print("The `claude` CLI is not on PATH.")
    except KeyboardInterrupt:
        pass
    if not file.exists():
        print(f"\n{t['id']} is no longer waiting; moving on.")
        return None
    try:
        updated = tasks.read(file)
    except json.JSONDecodeError as e:
        print(f"\n{file.relative_to(ROOT)} is no longer valid JSON ({e}); restoring the previous version.")
        tasks.write("proposed", t)
        return t
    print()
    print_proposal(updated, heading="Changed in the chat" if updated != t else "Back from the chat",
                   full_task=False)
    return updated


def cmd_decide(ids: list, approve: bool) -> None:
    known = tasks.all_tasks()
    approved = 0
    for tid in ids:
        status, t = known.get(tid, (None, None))
        if status != "proposed":
            print(f"{tid}: not a waiting proposal (status: {status or 'unknown'})")
            continue
        if approve:
            missing = [d for d in t.get("depends_on", []) if d not in known]
            models = load_config()["models"]
            if (missing or not (ROLES_DIR / f"{t['role']}.md").exists()
                    or (t.get("model") and t["model"] not in models["workers"])
                    or (t.get("effort") and t["effort"] not in models["efforts"])):
                print(f"{tid}: cannot approve (role '{t['role']}', model '{t.get('model')}', "
                      f"effort '{t.get('effort')}', unknown deps {missing})")
                continue
            tasks.move(tid, "proposed", "pending")
            approved += 1
            emit("task_approved", task=tid)
            print(f"{tid}: approved, queued as pending")
        else:
            tasks.move(tid, "proposed", "rejected")
            emit("task_rejected", task=tid)
            print(f"{tid}: rejected")
    if approved:
        print("Start (or keep) `orchestrator.py run` to work on approved tasks.")


def cmd_retry(ids: list) -> None:
    known = tasks.all_tasks()
    for tid in ids:
        status, _ = known.get(tid, (None, None))
        if status != "failed":
            print(f"{tid}: not failed (status: {status or 'unknown'})")
            continue
        tasks.move(tid, "failed", "pending", attempts=0)
        emit("task_requeued", task=tid, detail="manual retry")
        print(f"{tid}: queued again with fresh attempts")


# ---------------------------------------------------------------- status / reset

def print_status() -> None:
    rows = tasks.all_tasks()
    if not rows:
        print("No tasks. Use `plan` or `load` first.")
        return
    print(f"\n{'ID':<9}{'STATUS':<10}{'ROLE':<10}{'MODEL':<13}{'AGENT':<9}TITLE")
    for tid, (status, t) in sorted(rows.items()):
        model = "/".join(filter(None, [t.get("model"), t.get("effort")])) or "-"
        print(f"{tid:<9}{status:<10}{t['role']:<10}{model:<13}{t.get('agent') or '-':<9}{t['title']}")
        if status == "failed" and t.get("error"):
            print(f"{'':<32}error: {t['error'].splitlines()[0][:100]}")
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
    print("Removed .orch/, .worktrees/ and agent/* branches. story/* branches (your results) are kept; "
          "delete one with: git branch -D story/<plan>")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    plan = sub.add_parser("plan", help="story file (or one-line goal) -> plan file for review")
    plan.add_argument("story", help="path to a story .md file, or a one-line goal in quotes")
    plan.add_argument("--out", help="plan file to write (default backlog/plans/<story name>.json)")
    plan.add_argument("--prefix", help="task ID prefix (default from the story file name, e.g. S02)")
    sub.add_parser("load").add_argument("file")
    run = sub.add_parser("run")
    run.add_argument("--workers", type=int, default=load_config()["max_workers"])
    run.add_argument("--fake", action="store_true", help="dry run: workers write placeholders instead of calling Claude")
    run.add_argument("--only", default="", help="comma-separated task IDs: run just these (e.g. the spec, to review it first)")
    sub.add_parser("status")
    sub.add_parser("dashboard", help="serve the live dashboard (works with or without a run)")
    sub.add_parser("proposals")
    sub.add_parser("review", help="decide the waiting proposals one by one, interactively")
    sub.add_parser("approve").add_argument("ids", nargs="+")
    sub.add_parser("reject").add_argument("ids", nargs="+")
    sub.add_parser("retry", help="put failed tasks back in the queue").add_argument("ids", nargs="+")
    sub.add_parser("reset")
    args = ap.parse_args()

    if args.cmd == "plan":
        cmd_plan(args.story, args.out, args.prefix)
    elif args.cmd == "load":
        cmd_load(args.file)
    elif args.cmd == "run":
        cmd_run(args.workers, args.fake, [t for t in args.only.split(",") if t])
    elif args.cmd == "status":
        print_status()
    elif args.cmd == "dashboard":
        cmd_dashboard()
    elif args.cmd == "proposals":
        cmd_proposals()
    elif args.cmd == "review":
        cmd_review()
    elif args.cmd in ("approve", "reject"):
        cmd_decide(args.ids, approve=args.cmd == "approve")
    elif args.cmd == "retry":
        cmd_retry(args.ids)
    elif args.cmd == "reset":
        cmd_reset()


if __name__ == "__main__":
    main()
