"""Per-role MCP servers for agents.

config.json defines the servers ("mcp_servers") and which roles get them ("mcp").
Each agent runs with --strict-mcp-config, so it sees exactly its role's servers and
never the user's personal MCP setup.

String values may use placeholders: {root} (main checkout), {orch} (.orch/), {env:NAME}.
A server with "requires_env" is skipped when one of those variables is unset.
"""
from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

from common import ORCH, ROOT, sh

_PLACEHOLDER = re.compile(r"\{(root|orch|env:[A-Za-z_][A-Za-z0-9_]*)\}")


def expand(value: str) -> str:
    def repl(m: re.Match) -> str:
        key = m.group(1)
        if key == "root":
            return str(ROOT)
        if key == "orch":
            return str(ORCH)
        return os.environ.get(key[len("env:"):], "")
    return _PLACEHOLDER.sub(repl, value)


def servers_for(role: str, config: dict) -> tuple:
    """Returns ({name: server definition in Claude's mcpServers format}, [skipped reasons])."""
    defs = config.get("mcp_servers", {})
    servers, skipped = {}, []
    for name in config.get("mcp", {}).get(role, []):
        d = defs[name]
        missing = [v for v in d.get("requires_env", []) if not os.environ.get(v)]
        if missing:
            skipped.append(f"{name} (needs {', '.join(missing)})")
            continue
        env = {k: expand(v) for k, v in d.get("env", {}).items()}
        env.update({k: os.environ[k] for k in d.get("pass_env", []) if k in os.environ})
        server = {"type": "stdio", "command": expand(d["command"]), "args": [expand(a) for a in d.get("args", [])]}
        if env:
            server["env"] = env
        servers[name] = server
    return servers, skipped


def claude_args(config_file: Path, servers: dict) -> list:
    """CLI arguments that give an agent exactly these servers (none if empty)."""
    config_file.write_text(json.dumps({"mcpServers": servers}, indent=2))
    return ["--mcp-config", str(config_file), "--strict-mcp-config"]


def allowed_tools(servers: dict) -> list:
    """Permission rules allowing every tool of these servers (mcp__<server>)."""
    return [f"mcp__{name}" for name in servers]


def prepare(config: dict) -> None:
    """Run the one-time setup of every server some role uses (e.g. building a pinned binary)."""
    used = sorted({name for names in config.get("mcp", {}).values() for name in names})
    for name in used:
        cmd = config["mcp_servers"][name].get("prepare")
        if not cmd:
            continue
        print(f"Preparing MCP server '{name}'...")
        result = sh(expand(cmd), check=False)
        if result.returncode != 0:
            sys.exit(f"MCP server '{name}' setup failed:\n{result.stdout}{result.stderr}")
