# cl — a session picker for Claude Code

Date: 2026-09-03

## Problem

`claude` starts a new session. Returning to an earlier conversation means
`claude -r`, whose picker is scoped to the current directory and offers no
model choice. With 149 transcripts across 24 project directories, finding "the
one about the flaky auth test" is guesswork.

## Goal

One command, `cl`, that lists resumable sessions with readable titles, groups
them by proximity to where you are, lets you pick a model in the same breath,
and defaults to starting something new. Fast enough to type reflexively.

## What the data already gives us

Verified against the live store before designing anything:

- Sessions are `~/.claude/projects/<munged-cwd>/<uuid>.jsonl`. Of 261 `.jsonl`
  files, 112 are subagent sidechains in nested `<uuid>/subagents/`
  directories; 149 are real sessions, totalling 277MB, largest 63MB.
- Claude writes an `ai-title` record — a real sentence describing the
  conversation — but only 30 of 149 sessions have one. When present it is
  always within the last 64KB.
- Every user and assistant record carries `cwd`, `gitBranch` and
  `message.model`. The first human prompt is within the first 64KB for 113 of
  120 sessions that have one; widening to 1MB gains only two more.
- `claude` already supports `--resume <id>`, `--fork-session`, `--model` and
  `--session-id`.

## Design

### Metadata extraction

Read a 64KB head window and a 64KB tail window per transcript; read files under
256KB whole. Within a window, prefilter each line for a byte marker
(`"type":"ai-title"`, `"type":"assistant"`, …) before `json.Unmarshal`, so
parsing never touches the megabytes of tool output in between. Partial records
at window edges are dropped.

Head yields the opening prompts and `cwd`; tail yields the freshest `ai-title`,
the model last used, and the preview turns. `cwd` falls back to the tail for
sessions whose head is entirely hook records, and then to a sibling session in
the same project directory — every transcript there was recorded from the same
directory, which is exact rather than guessing at the mangled directory name.

Measured alternatives:

| Strategy | Cost | Decision |
|---|---|---|
| Full read and parse | 231ms warm, worse cold | rejected |
| Head + tail windows | ~40ms for the whole store | **chosen** |
| Windows plus an on-disk cache | ~5ms | rejected: saves 35ms, adds staleness bugs |

Sessions with no human turn, or with no resolvable working directory, are
omitted — there is nothing in them to resume.

#### Codex rollouts

Codex streams complete records through its own reader rather than using the
Claude head/tail windows. A human prompt or recent turn context may be several
megabytes from either end; completeness takes priority over a fixed scan budget.
At most four workers read files concurrently. The reader inspects envelope
fields in the first record fragment and drains irrelevant tool/reasoning
records without buffering or decoding them. Undecidable prefixes fall back to
complete records, including unusual field ordering. Relevant records still
receive full JSON validation. Memory holds a reader buffer, at most one relevant
record, and display metadata per worker, not complete rollouts.

The first nonempty rollout record must be a valid session header and owns
identity, branch, fork parent, and source. A malformed initial header rejects
the file; later inherited headers cannot repair or replace it. Known subagent and internal
sources are excluded from the picker. The latest usable turn context supplies
model and working directory; older message-model fields are a compatibility
fallback. Only the opening prompt candidates and recent preview turns are kept.

Known context at message/line boundaries is removed before display markup,
including trailing context, skill manuals, shell output, plugin lists, and
subagent status notices. Unclosed machine wrappers are dropped; literal inline
tag examples remain searchable. An AGENTS manual preserves an appended request
after its closing INSTRUCTIONS tag or explicit request separator; blank
paragraphs alone do not distinguish requests from instructions. Paseo metadata
generation calls are excluded by originator and prompt template, while genuine
Paseo sessions remain. Title candidates still use the shared bestTitle ranking.
Malformed non-header records and incomplete final writes are ignored. Claude's
bounded scanner and the no-cache policy are unchanged.

### Titles

`ai-title` when present, marked with a `·`. Otherwise the opening prompt,
chosen by: skip prompts under 18 runes (sessions often open "ok", "resume",
"Yoo!" and carry the real request a turn later), prefer prose over pasted
material (URLs, `KEY=value`, low letter or space ratio), and fall back through
pasted, then short, so a title is empty only when there were no prompts.
Sessions whose opening prompts are all weak get one bounded 2MB deeper scan;
only a handful of sessions pay it. Scheduled runs are titled from the
`scheduled-task name="…"` attribute.

Injected wrappers — system reminders, command output, hook results — are
stripped with their contents. RE2 has no backreferences, so each wrapper name
gets its own attribute-tolerant pattern; content inside unrecognised tags is
kept rather than guessed at.

### Sections

`HERE` (exact cwd), `REPO` (the rest of the git repository found by walking up
for `.git`, accepting the file form so linked worktrees resolve to themselves),
`ALL PROJECTS` (everything else, capped at 40). Empty sections are dropped
except `HERE`, which always exists because it holds the `New session` row.

The cwd is resolved through `filepath.EvalSymlinks`: Claude records the resolved
path, so on macOS an unresolved `/tmp` would file every `/private/tmp` session
under `ALL PROJECTS`.

### Model selection

A persistent footer list, cycled with `←→`. It follows the highlighted row, so
`⏎` continues a conversation on the model it was having; `New session` uses the
`model` from `settings.json`, which is placed first in the list. Pressing `←→`
pins the choice and stops it following the cursor. Recorded ids map to aliases
(`claude-opus-4-7` → `opus`) so the picker keeps working as models ship;
unrecognised ids leave the configured default in charge.

### Launch

`syscall.Exec` replaces the process, so no wrapper sits between the terminal and
claude. Resume and fork `chdir` into the session's directory first.

Note on that chdir, corrected during implementation: `--resume` locates a
session by id from *any* directory — verified with a throwaway session. The
chdir is still required, for a different reason: resuming elsewhere runs the
conversation against the wrong `CLAUDE.md`, relative paths and git repository,
and appends records carrying that other directory to the transcript (observed:
7 such records after one wrong-directory resume).

### Search

Each whitespace-separated term must match, either as a substring of
title + directory + branch + model, or as a *local* subsequence of the title —
a subsequence held within a span of `max(3n, n+6)` runes, and only for terms of
three runes or more. The locality cap is what makes it meaningful: an
unconstrained subsequence search over title + path + model let "heap" match
"add webhook retry" by collecting letters from the directory and the model
name.

### Deletion

`d` confirms, then *moves* the transcript to `~/.claude/.trash-cl/<date>/`,
namespaced by project directory, taking the session's subagent directory with
it. Transcripts hold hours of conversation and tens of megabytes; a keystroke
should not destroy that irreversibly.

## Testing

Parsing, grouping, filtering, title selection and argv construction are pure
functions over bytes and structs, covered by table-driven tests including
truncated window lines, malformed JSON, missing `ai-title`, sessions with no
human turn, worktree `.git` files, unicode, sibling-directory paths
(`/repo-tools` must not count as inside `/repo`) and a malformed settings file.

The TUI is tested by feeding `KeyMsg` sequences to `Update` and asserting state:
navigation skips headers and stops at the ends, `⏎` distinguishes new from
resume, fork refuses the `New session` row, delete asks first, search filters
and `esc` restores, and `View` renders at five terminal sizes and in every mode.

Two live tests read the real store: one asserts the scan stays under 500ms and
that titles and directories resolve, one prints a real frame for eyeballing.
`Exec` is tested for real in a child process against a stand-in `claude` that
reports its argv and working directory.

## Out of scope for v1

Background sessions (`claude --bg`, `claude agents`), cloud sessions, renaming
sessions, and multi-select.
