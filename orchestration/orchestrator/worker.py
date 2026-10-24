"""Worker loop: claim a task -> run a Claude agent in its worktree -> commit -> quality gate.

Started by orchestrator.py: python3 orchestration/orchestrator/worker.py --id agent-1 [--fake] [--only IDS]
"""
from __future__ import annotations

import argparse
import json
import subprocess
import threading
import time
from pathlib import Path

import mcp
import quality_gate
import stories
import tasks
import worktrees
from common import (AGENTS_DIR, LOGS_DIR, MCP_DIR, PROPOSALS_FILE, ROLES_DIR, STOP_FILE, emit, load_config,
                    model_args, tool_env, worker_model)


class Worker:
    def __init__(self, agent_id: str, fake: bool, only: list = None):
        self.id = agent_id
        self.fake = fake
        self.only = set(only or [])
        self.config = load_config()

    def heartbeat(self, status: str, task: dict = None) -> None:
        (AGENTS_DIR / f"{self.id}.json").write_text(json.dumps({
            "id": self.id, "status": status, "updated": time.time(),
            "task": task["id"] if task else None,
            "role": task["role"] if task else None,
            "model": worker_model(task, self.config)[0] if task else None,
        }))

    def loop(self) -> None:
        emit("agent_start", agent=self.id)
        while not STOP_FILE.exists():
            task = tasks.claim(self.id, self.only)
            if task is None:
                self.heartbeat("idle")
                time.sleep(self.config["poll_interval_s"])
                continue
            self.run_task(task)
        self.heartbeat("stopped")
        emit("agent_stop", agent=self.id)

    def run_task(self, task: dict) -> None:
        tid = task["id"]
        emit("task_claimed", agent=self.id, task=tid, attempt=task["attempts"])
        self.heartbeat("working", task)
        try:
            # Tasks start from (and are later merged into) their plan's story branch.
            self.config["base_branch"] = stories.branch(stories.plan_of(task))
            wt = worktrees.create(tid, self.config["base_branch"])
            if self.fake:
                self.fake_agent(task, wt)
            else:
                self.claude_agent(task, wt)

            # .orch-proposals.json is gitignored, so it stays out of the commit.
            if not worktrees.commit_all(wt, f"{tid}: {task['title']}"):
                raise RuntimeError("agent made no changes")

            self.heartbeat("gating", task)
            ok, reason = quality_gate.check(task, wt, self.config, run_commands=not self.fake)
            if not ok:
                raise RuntimeError(reason)
            # Only a passing attempt's proposals count; a failed attempt's are discarded with its worktree.
            self.collect_proposals(task, wt)
        except Exception as e:  # noqa: BLE001 - any failure sends the task to failed/
            tasks.move(tid, "claimed", "failed", error=str(e))
            emit("task_failed", agent=self.id, task=tid, error=str(e)[:500])
            return

        tasks.move(tid, "claimed", "passed")
        emit("task_passed", agent=self.id, task=tid, detail=reason)

    def collect_proposals(self, task: dict, wt: Path) -> None:
        """Move follow-up tasks the agent proposed out of the worktree into proposed/."""
        f = wt / PROPOSALS_FILE
        if not f.exists():
            return
        try:
            proposals = json.loads(f.read_text())
            ids, errors = tasks.propose(task, proposals if isinstance(proposals, list) else [proposals])
        except json.JSONDecodeError as e:
            ids, errors = [], [f"{PROPOSALS_FILE} is not valid JSON: {e}"]
        f.unlink()
        for tid in ids:
            emit("task_proposed", agent=self.id, task=tid, detail=f"proposed by {task['id']}")
        for err in errors:
            emit("proposal_invalid", agent=self.id, task=task["id"], error=err)

    def build_prompt(self, task: dict) -> str:
        scope = "\n".join(f"  - {s}" for s in task.get("scope", [])) or "  - (any)"
        return (
            f"Task {task['id']}: {task['title']}\n\n"
            f"{task['prompt']}\n\n"
            f"You may ONLY create or modify files matching:\n{scope}\n\n"
            "Run the relevant checks before finishing. Do not commit; the orchestrator commits for you.\n"
            f"Work that is needed but outside your scope: propose it as follow-up tasks in {PROPOSALS_FILE} "
            "(see CLAUDE.md); a human approves them."
        )

    def claude_agent(self, task: dict, wt: Path) -> None:
        agent_cfg = self.config["agent"]
        role_prompt = (ROLES_DIR / f"{task['role']}.md").read_text()
        attempt = f"{task['id']}.attempt{task['attempts']}"
        alias, model = worker_model(task, self.config)
        first = worker_model({**task, "attempts": 1}, self.config)
        emit("model", agent=self.id, task=task["id"],
             detail=f"{alias} ({model['id']}), effort {model['effort'] or 'default'}"
             + (" escalated after a failed attempt" if (alias, model) != first else ""))
        servers, skipped = mcp.servers_for(task["role"], self.config)
        emit("mcp", agent=self.id, task=task["id"],
             detail=f"servers: {', '.join(servers) or 'none'}" + (f"; skipped {', '.join(skipped)}" if skipped else ""))
        cmd = [
            "claude", "-p", self.build_prompt(task),
            "--append-system-prompt", role_prompt,
            *model_args(model),
            "--permission-mode", agent_cfg["permission_mode"],
            "--allowedTools", *agent_cfg["allowed_tools"], *mcp.allowed_tools(servers),
            *mcp.claude_args(MCP_DIR / f"{attempt}.json", servers),
            "--output-format", "stream-json", "--verbose",
        ]
        log_path = LOGS_DIR / f"{attempt}.jsonl"
        proc = subprocess.Popen(cmd, cwd=wt, env=tool_env(), text=True,
                                stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        timer = threading.Timer(agent_cfg["timeout_s"], proc.kill)
        timer.start()
        try:
            with log_path.open("w") as log:
                for line in proc.stdout:
                    log.write(line)
                    log.flush()
                    self.relay(task, line)
            proc.wait()
        finally:
            timer.cancel()
        if proc.returncode != 0:
            raise RuntimeError(f"claude exited with {proc.returncode} (see {log_path.name})")

    def relay(self, task: dict, line: str) -> None:
        """Forward tool calls and the final result from Claude's stream to the event log."""
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            return
        if msg.get("type") == "assistant":
            for block in msg.get("message", {}).get("content", []):
                if block.get("type") == "tool_use":
                    inp = block.get("input", {})
                    detail = inp.get("file_path") or inp.get("command") or inp.get("pattern") or ""
                    emit("tool", agent=self.id, task=task["id"],
                         detail=f"{block['name']} {str(detail)[:120]}")
        elif msg.get("type") == "result":
            emit("agent_result", agent=self.id, task=task["id"],
                 cost_usd=msg.get("total_cost_usd"), turns=msg.get("num_turns"))

    def fake_agent(self, task: dict, wt: Path) -> None:
        """Dry-run stand-in for Claude: writes one placeholder file inside the task's scope."""
        time.sleep(2)
        pattern = (task.get("scope") or ["docs/**"])[0]
        target = wt / pattern.replace("**/", "").replace("**", "*").replace("*", f"FAKE_{task['id']}", 1)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(f"Placeholder for {task['id']}: {task['title']}\n")
        emit("tool", agent=self.id, task=task["id"], detail=f"Write {target.relative_to(wt)}")
        if task["role"] == "security":  # exercise the proposal/approval flow in dry runs
            (wt / PROPOSALS_FILE).write_text(json.dumps([{
                "title": f"Fake finding from {task['id']}", "role": "backend", "severity": "medium",
                "prompt": "Placeholder fix.", "scope": ["docs/**"],
                "rationale": "Dry-run proposal; approve it to see it run.",
            }]))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--id", required=True)
    ap.add_argument("--fake", action="store_true", help="don't call Claude; write placeholder files")
    ap.add_argument("--only", default="", help="comma-separated task IDs this worker may claim")
    args = ap.parse_args()
    Worker(args.id, args.fake, [t for t in args.only.split(",") if t]).loop()


if __name__ == "__main__":
    main()
