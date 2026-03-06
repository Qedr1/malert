# AGENT RULES
## Priority And Terms
These AGENT RULES have top priority in this repository and override default assistant behavior. `pre-reason` = first assistant action for a new user request before analysis/tools/edits; `inspection` = read-only files and commands only; updates to `oper. docs` need no separate approval only when they reflect progress within an already approved `P#` scope.

AGENTS: Before pre-reason, always read and follow all rules in AGENTS.md.

## Tone
Default to minimal output. 
Concise; essentials. Violation=>STOP; ack; validate ans; fix; continue.

## Docs
### Agent docs
If any docs/state/*.md file is missing:  create it.
docs/state/context.md: global current state for the project (agent-only source of truth). English only, very compact brief context, but very complete meaning. Contains: stack/versions, architecture/flow, data contracts/schemas, config model + defaults, invariants/constraints, known issues/decisions, and the current summary plan ("Project plan"). Update "Project plan" in docs/state/context.md only after user confirms solved/accepted; Read first code analysis.Edit only on explicit user command ("update context" / "обнови контекст"); otherwise read-only.
docs/state/detailed_plan.md: oper. doc; agent-only detailed roadmap (agreed). English only. Detailed plan by each item of summary Project plan. Update during work as status changes (OPEN/DONE + brief notes/next). Keep IDs stable (Pn). Default OPEN; set to DONE only after tests pass; if regressions/errors appear, revert item to OPEN. After finishing the Plan => ask confirmation => update docs/state/context.md.
docs/state/skill.md: oper. doc; is the ONLY operational source for knowledge: run/build/test/restart commands,troubleshooting steps, incident fixes and verification checks. Read on every task and before any restart/retest; Update only when a runtime/build/test/reload issue is reproduced and a fix is verified with exact commands; using compact entries: Problem|Symptoms|Ready fix(exact commands/files)|Verify(exact checks)|Status(active/resolved/superseded)|Last verified(YYYY-MM-DD HH:MM); Create it if file missing.
docs/state/changelog.md:oper. doc; English only, agent-only; fmt `YYYY-MM-DD HH:MM: why, what(file:lines)->result`; brief; write a changelog entry whenever the detailed plan status changes; read last 10 lines before pre-reason.

### User docs
docs/README.md: for user only tech spec; ask=>edit; if docs/state/context.md file missing or my request; 

## Planning
Before any restart/retest/run command: re-read docs/state/skill.md; apply all relevant Ready fix entries (if none, use "Skill check: none"); then print in commentary exactly: "Skill check: <entry-or-none>|<applied command(s)>|<verify>". If not printed -> STOP.
Execution gate (STRICT): any P# is DRAFT until explicit user approval.
Before any non-inspection command outside oper. docs: output Q#/P#, ask approval, wait.
Approval signals: "approve P#n", "да", "ок", "+". Any other response or no response = not approved.
Order for restart/retest/run: Skill check -> Q#/P# -> explicit user approval -> execute command.
If agent has unanswered questions: no plan; ask Q# first.
Questions only if missing info; otherwise provide a DRAFT plan and request approval.
Blocks Plan numbered P#n. Blocks questions numbered Q#n. Items inside each block: 1..n. New Plan ID = last P# + 1. New Question ID = last Q# + 1. Never reset numbering.
No ask me: read/inspect docs/* and source files; update docs/state/changelog.md and docs/state/detailed_plan.md for bookkeeping only.
Ask me: execute any P#; new external libraries; new/change project stack; after plan finish (confirmation).
Partial approval => execute only explicitly approved items; repeat remaining Q/P.
Unknown stack/env=>ask me; never guess. 
Check/inspect/review=>audit: read whole files; syntax/lint; compare specs; list defects+risks before proposing changes.
"Detailed plan" in docs/state/detailed_plan.md is updated as work progresses (status/steps done/next) only for approved scope.

## Code
Validate vs installed; if unknown=>ask.
Write new code or any new entity only if no existing one can be reused.
Func comment: purpose/params/return.
Shortest path: right types for domain; min abstractions; explicit over implicit; fail fast at boundaries.
DRY: extract repeats; unify ops (logs/DB/errors etc).
Errors: handle explicitly (return/log/fail); never silent. Bash redirects. Reported error=>trace all paths.
No global mutable state; avoid shared mutable state across goroutines; pass dependencies explicitly. OK: file-scope consts/enums/typed errors; explicit documented caches only; instance fields in structs.
Single responsibility; name = verb+noun (load/parse/validate, not process/handle).
Logs: info/warn/error/panic/debug; fmt from cfg(line|json); console short and colors (line format only), no colors for json; file full no colors.
Libs: new=>ask me; prefer popular; custom only if no public option.
