"""Checks a task branch must pass before it can be merged."""
from __future__ import annotations

import posixpath
from fnmatch import fnmatch
from pathlib import Path

import worktrees
from common import sh, tool_env


def check(task: dict, wt: Path, config: dict, run_commands: bool = True) -> tuple:
    """Returns (ok, reason). run_commands=False only checks the diff (used by --fake)."""
    changed = worktrees.changed_files(task["id"], config["base_branch"])
    if not changed:
        return False, "no changes"

    scope = task.get("scope", [])
    outside = [f for f in changed if scope and not any(fnmatch(f, pat) for pat in scope)]
    if outside:
        return False, f"changed files outside scope {scope}: {outside}"

    untested = untested_changes(changed, config.get("require_tests_for", []))
    if untested:
        return False, f"code changed without a test change in the same package (TDD): {untested}"

    for gate in config["gates"] if run_commands else []:
        if not any(fnmatch(f, pat) for f in changed for pat in gate["paths"]):
            continue
        result = sh(gate["cmd"], cwd=wt / gate["cwd"], check=False, env=tool_env())
        if result.returncode != 0:
            tail = (result.stdout + result.stderr).strip()[-1500:]
            return False, f"gate '{gate['name']}' failed:\n{tail}"

    return True, f"{len(changed)} file(s) changed"


def untested_changes(changed: list, patterns: list) -> list:
    """Changed non-test files matching patterns whose directory has no changed _test.go file."""
    tested_dirs = {posixpath.dirname(f) for f in changed if f.endswith("_test.go")}
    return [f for f in changed
            if not f.endswith("_test.go")
            and any(fnmatch(f, pat) for pat in patterns)
            and posixpath.dirname(f) not in tested_dirs]
