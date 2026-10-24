# S01: Manage todos

<!-- The story behind the prepared demo plan orchestration/plans/todo-tasks.json (task IDs T-001…T-010). -->

## Story

As a busy person, I want to keep a list of todos that I can complete and reopen, so that I know what's left to do.

## Domain

- **Bounded context:** `todo` (new)
- **Terms:** a *todo* has a *title*; it is *open* until *completed* and can be *reopened*; todos are *added*, *renamed* and *removed*.
- **Rules:**
  - A title is 1–200 characters after trimming whitespace.
  - A completed todo cannot be renamed; reopen it first.
  - Completing or reopening twice is harmless.

## Acceptance criteria

- [ ] Given no todos, when I list todos, then I get an empty list.
- [ ] When I add a todo with title "Buy milk", then it is listed, open, with an ID.
- [ ] When I add a todo with an empty, whitespace-only or 201-character title, then I get 400.
- [ ] Given an open todo, when I complete it, then it is shown as done; when I reopen it, it is open again.
- [ ] Given a completed todo, when I rename it, then I get 409; when I reopen and rename it in one update, it succeeds.
- [ ] When I get, change or remove a todo that doesn't exist, then I get 404.
- [ ] Todos survive a restart when the app runs with MongoDB (`make up`).

## Out of scope

- Authentication and authorization.
- Due dates, priorities, sharing.
