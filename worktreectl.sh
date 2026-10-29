#!/usr/bin/env bash
# worktreectl — start and clean up git worktrees for parallel work by agents or humans.
#
# Works with bash 3.2 (macOS default) and Linux. Needs git >= 2.17.
# Optional: lsof (process/port detection on macOS), docker (+ compose v2).
#
# Design rules (agent-friendly):
#   * never prompts; refuses risky actions with a clear message and non-zero exit
#   * stdout carries data only (paths, tables); logs go to stderr
#   * every destructive command supports -n/--dry-run
set -u -o pipefail

VERSION=0.1.0
PROG=${0##*/}

: "${WORKTREECTL_BRANCH_PREFIX:=wt/}"
: "${WORKTREECTL_KILL_TIMEOUT:=10}"
: "${WORKTREECTL_PORT_BASE:=4000}"
: "${WORKTREECTL_MATCH_CMDLINE:=0}"

DRY=0
TMPD=""
# Only protect the caller's process group when our output really feeds a pipe.
PIPED=0
{ [ -p /dev/stdout ] || [ -p /dev/stderr ]; } 2>/dev/null && PIPED=1
DOCKER_STATE=""

usage() {
  cat <<EOF
$PROG $VERSION — start and clean up git worktrees

Usage: $PROG <command> [options]

Start work
  new [NAME] [opts] [-- CMD...]   Create a worktree + branch, copy local files, install deps.
                                  Prints the path on stdout; with -- CMD, runs CMD inside it.
      -b, --base REF                start from REF (default: current HEAD)
          --branch NAME             branch name (default: ${WORKTREECTL_BRANCH_PREFIX}NAME; reused if it exists)
      -d, --detach                  detached HEAD, no branch (Codex style)
          --fetch                   git fetch before creating
          --no-copy                 don't copy .worktreeinclude files
          --no-setup                skip setup hook / dependency install
  exec [NAME] [-- CMD...]         Run CMD (default: \$SHELL) in the worktree with .worktree.env loaded
  path [NAME]                     Print the worktree path

Inspect
  list                            All worktrees: branch, clean/dirty, commits ahead of base, processes
  ps [NAME]                       Processes (with listening ports) and docker containers tied to it

Clean up
  stop [NAME] [-n]                Stop its processes and containers, keep the worktree
  rm   [NAME] [-n] [-f] [--keep-branch] [--volumes]
                                  Teardown hook, stop processes and containers, remove the worktree,
                                  delete the branch if merged. Refuses with uncommitted changes
                                  unless -f (which also force-deletes the branch).
  gc   [-n] [--all]               rm every clean, fully merged, idle worktree under the root
                                  (--all: also worktrees created outside the root)

NAME can be the worktree name, its path, or its branch. When omitted, the worktree
containing the current directory is used.

What counts as "started from the worktree"
  * processes whose cwd is inside it (dev servers, watchers, test runners, agents)
    and, with WORKTREECTL_MATCH_CMDLINE=1, processes whose command line mentions it.
    This command's own parent chain (e.g. the agent calling it) is never touched.
  * docker compose projects whose working dir is inside it (stopped / 'compose down')
  * containers bind-mounting a path inside it

Files (in the repo)
  .worktreeinclude          gitignored files to copy into new worktrees, one glob per line
                            (same file Claude Code reads; default if absent: .env .env.*)
  .worktreectl/setup        runs in a new worktree instead of the dependency auto-install
  .worktreectl/teardown     runs before 'rm' stops things (drop a test DB, etc.)
  .worktree.env             written into each worktree: WORKTREE_NAME, WORKTREE_PATH, WORKTREE_MAIN,
                            WORKTREE_BRANCH, WORKTREE_PORT, PORT, COMPOSE_PROJECT_NAME

Environment
  WORKTREECTL_ROOT           where worktrees go (default: <repo-parent>/<repo>.worktrees;
                             relative values are relative to the main checkout)
  WORKTREECTL_BASE           base ref for 'ahead' and 'gc' (default: main checkout's branch)
  WORKTREECTL_BRANCH_PREFIX  default: wt/
  WORKTREECTL_PORT_BASE      first port handed out, in steps of 10 (default: 4000)
  WORKTREECTL_KILL_TIMEOUT   seconds between SIGTERM and SIGKILL (default: 10)
  WORKTREECTL_MATCH_CMDLINE=1  also match processes by command line (off by default: it
                             would catch e.g. 'vim <worktree>/file' opened from elsewhere)
  WORKTREECTL_NO_DOCKER=1    skip docker checks
EOF
}

# ---------------------------------------------------------------- helpers
info() { printf '%s: %s\n' "$PROG" "$*" >&2; }
warn() { printf '%s: warning: %s\n' "$PROG" "$*" >&2; }
die()  { printf '%s: error: %s\n' "$PROG" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
run()  { if [ "$DRY" = 1 ]; then info "[dry-run] $*"; else "$@"; fi; }
phys() { (cd "$1" 2>/dev/null && pwd -P) || printf '%s' "$1"; }
cleanup() { if [ -n "$TMPD" ]; then rm -rf "$TMPD"; fi; }
trap cleanup EXIT

init_repo() {
  git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"
  MAIN=$(git worktree list --porcelain | awk '/^worktree /{print substr($0,10); exit}')
  [ -n "$MAIN" ] || die "cannot find the main worktree"
  REPO_NAME=${MAIN##*/}
  ROOT=${WORKTREECTL_ROOT:-$(dirname "$MAIN")/$REPO_NAME.worktrees}
  case $ROOT in /*) ;; *) ROOT="$MAIN/$ROOT" ;; esac
  ROOT=${ROOT%/}
  COMMON=$(git -C "$MAIN" rev-parse --git-common-dir)
  case $COMMON in /*) ;; *) COMMON="$MAIN/$COMMON" ;; esac
  if [ -n "${WORKTREECTL_BASE:-}" ]; then
    BASEREF=$WORKTREECTL_BASE
  else
    BASEREF=$(git -C "$MAIN" symbolic-ref --quiet --short HEAD 2>/dev/null || git -C "$MAIN" rev-parse HEAD)
  fi
}

# Lines: path|branch-or-dash|head
wt_table() {
  git -C "$MAIN" worktree list --porcelain | awk '
    function flush() { if (p != "") print p "|" (b == "" ? "-" : b) "|" h }
    /^worktree / { flush(); p = substr($0, 10); b = ""; h = "" }
    /^HEAD /     { h = substr($0, 6) }
    /^branch /   { b = substr($0, 8); sub(/^refs\/heads\//, "", b) }
    END          { flush() }'
}

# Sets T_PATH, T_BRANCH, T_HEAD
resolve() {
  local arg=${1:-} abs="" p b h
  if [ -z "$arg" ]; then
    arg=$(git rev-parse --show-toplevel 2>/dev/null) || die "no worktree given"
  fi
  [ -d "$arg" ] && abs=$(phys "$arg")
  while IFS='|' read -r p b h; do
    [ -n "$p" ] || continue
    if [ "$p" = "$arg" ] || [ "${p##*/}" = "$arg" ] || [ "$b" = "$arg" ] ||
       [ "$b" = "$WORKTREECTL_BRANCH_PREFIX$arg" ] ||
       { [ -n "$abs" ] && [ "$(phys "$p")" = "$abs" ]; }; then
      T_PATH=$p; T_BRANCH=$b; T_HEAD=$h
      return 0
    fi
  done <<EOF
$(wt_table)
EOF
  die "no worktree matches '$arg' (see: $PROG list)"
}

not_main() { [ "$T_PATH" != "$MAIN" ] || die "refusing to act on the main checkout ($MAIN)"; }

add_exclude() {
  local f="$COMMON/info/exclude"
  mkdir -p "$COMMON/info"
  grep -qxF "$1" "$f" 2>/dev/null || printf '%s\n' "$1" >>"$f"
}

load_env() {
  if [ -f "$1/.worktree.env" ]; then set -a; . "$1/.worktree.env"; set +a; fi
}

# ---------------------------------------------------------------- processes
snapshot() {
  [ -n "$TMPD" ] || TMPD=$(mktemp -d "${TMPDIR:-/tmp}/worktreectl.XXXXXX")
  ps -A -ww -o pid= -o ppid= -o pgid= -o command= >"$TMPD/ps" 2>/dev/null ||
    ps -ax -o pid= -o ppid= -o pgid= -o command= >"$TMPD/ps" 2>/dev/null
  : >"$TMPD/cwd"
  if [ -e /proc/self/cwd ]; then
    local d c
    for d in /proc/[0-9]*; do
      c=$(readlink "$d/cwd" 2>/dev/null) && printf '%s\t%s\n' "${d#/proc/}" "$c"
    done >"$TMPD/cwd"
  elif have lsof; then
    lsof -n -P -w -d cwd -Fpn 2>/dev/null |
      awk '/^p/{pid=substr($0,2)} /^n/{print pid "\t" substr($0,2)}' >"$TMPD/cwd"
  fi
}

# "pid<TAB>command" for processes tied to $1, excluding this script's ancestors/descendants.
# Processes in the caller's process group (e.g. the other end of `worktreectl rm | tee log`)
# are not returned either when our output is piped; they are written to $TMPD/skipped instead.
match_pids() {
  local root=$1
  : >"$TMPD/skipped"
  awk -v r1="$root" -v r2="$(phys "$root")" -v self="$$" -v bycmd="$WORKTREECTL_MATCH_CMDLINE" \
      -v cwdf="$TMPD/cwd" -v skipf="$TMPD/skipped" -v piped="$PIPED" '
    function under(s)    { return s == r1 || s == r2 || index(s, r1 "/") == 1 || index(s, r2 "/") == 1 }
    function mentions(s) { return index(s, r1 "/") > 0 || index(s, r2 "/") > 0 || s ~ ("[ =]" r1 "$") }
    FILENAME == cwdf { t = index($0, "\t"); cwd[substr($0, 1, t - 1)] = substr($0, t + 1); next }
    { pid = $1; pp[pid] = $2; pg[pid] = $3; c = $0; sub(/^[ \t]*[0-9]+[ \t]+[0-9]+[ \t]+[0-9]+[ \t]*/, "", c); cmd[pid] = c; ord[++n] = pid }
    END {
      for (q = self; q != "" && q != "0" && !(q in anc); q = pp[q]) anc[q] = 1
      for (i = 1; i <= n; i++) {
        pid = ord[i]
        if (pid in anc) continue
        desc = 0; d = 0
        for (q = pp[pid]; q != "" && q != "0" && d < 256; q = pp[q]) { if (q == self) { desc = 1; break }; d++ }
        if (desc) continue
        if (((pid in cwd) && under(cwd[pid])) || (bycmd == 1 && mentions(cmd[pid]))) {
          if (piped == 1 && pg[pid] == pg[self]) print pid "\t" cmd[pid] > skipf
          else print pid "\t" cmd[pid]
        }
      }
    }' "$TMPD/cwd" "$TMPD/ps"
}

alive() { kill -0 "$1" 2>/dev/null && ! ps -o stat= -p "$1" 2>/dev/null | grep -q '^Z'; }

pids_for() {
  local line
  match_pids "$1" | while IFS= read -r line; do
    alive "${line%%	*}" && printf '%s\n' "$line"
  done
}

ports_of() {
  have lsof || return 0
  lsof -n -P -a -p "$1" -iTCP -sTCP:LISTEN -Fn 2>/dev/null | sed -n 's/^n//p' | sort -u | tr '\n' ' ' | sed 's/ $//'
}

kill_pids() {
  local p still i=0 max=$((WORKTREECTL_KILL_TIMEOUT * 4))
  kill -TERM "$@" 2>/dev/null
  while :; do
    still=""
    for p in "$@"; do alive "$p" && still="$still $p"; done
    [ -z "$still" ] && return 0
    [ "$i" -ge "$max" ] && break
    sleep 0.25; i=$((i + 1))
  done
  warn "still running after ${WORKTREECTL_KILL_TIMEOUT}s, sending SIGKILL:$still"
  kill -KILL $still 2>/dev/null
  return 0
}

# ---------------------------------------------------------------- docker
docker_ok() {
  if [ -z "$DOCKER_STATE" ]; then
    DOCKER_STATE=no
    if [ "${WORKTREECTL_NO_DOCKER:-0}" != 1 ] && have docker && docker info >/dev/null 2>&1; then
      DOCKER_STATE=yes
    fi
  fi
  [ "$DOCKER_STATE" = yes ]
}

dc() {
  if docker compose version >/dev/null 2>&1; then docker compose "$@"
  elif have docker-compose; then docker-compose "$@"
  else return 127; fi
}

compose_projects() {
  docker ps -a --filter label=com.docker.compose.project \
    --format '{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.project.working_dir"}}' 2>/dev/null |
    awk -F'|' -v r1="$1" -v r2="$(phys "$1")" \
      '$2 == r1 || $2 == r2 || index($2, r1 "/") == 1 || index($2, r2 "/") == 1 { print $1 }' | sort -u
}

# "id|name|running" for containers bind-mounting from $1, minus compose projects listed in $2
mount_containers() {
  local ids
  ids=$(docker ps -aq 2>/dev/null)
  [ -n "$ids" ] || return 0
  docker inspect -f '{{.Id}}|{{.Name}}|{{.State.Running}}|{{index .Config.Labels "com.docker.compose.project"}}{{range .Mounts}}|{{.Source}}{{end}}' $ids 2>/dev/null |
    awk -F'|' -v r1="$1" -v r2="$(phys "$1")" -v skip=" $2 " '
      function under(s) { return s == r1 || s == r2 || index(s, r1 "/") == 1 || index(s, r2 "/") == 1 }
      $4 != "" && index(skip, " " $4 " ") > 0 { next }
      { for (i = 5; i <= NF; i++) if (under($i)) { print substr($1, 1, 12) "|" substr($2, 2) "|" $3; break } }'
}

# ---------------------------------------------------------------- stop everything
# $1 = worktree path, $2 = stop | rm
stop_all() {
  local root=$1 mode=$2 lines pids p projs c id cname running ids
  snapshot
  lines=$(pids_for "$root")
  if [ -n "$lines" ]; then
    printf '%s\n' "$lines" | while IFS= read -r c; do info "process ${c%%	*}: ${c#*	}"; done
    pids=$(printf '%s\n' "$lines" | cut -f1 | tr '\n' ' ')
    run kill_pids $pids
  else
    info "no processes running from $root"
  fi
  if [ -s "$TMPD/skipped" ]; then
    warn "left running (same process group as the caller, e.g. a pipe reader): $(cut -f1 "$TMPD/skipped" | tr '\n' ' ')- stop them yourself if needed"
  fi

  if ! docker_ok; then
    [ "${WORKTREECTL_NO_DOCKER:-0}" = 1 ] || ! have docker || warn "docker daemon not reachable, skipped container checks"
    return 0
  fi
  projs=$(compose_projects "$root" | tr '\n' ' ')
  for p in $projs; do
    if [ "$mode" = rm ]; then
      info "compose project '$p': down"
      run dc -p "$p" down --remove-orphans $VOLFLAG >&2 || {
        ids=$(docker ps -aq --filter "label=com.docker.compose.project=$p")
        [ -n "$ids" ] && run docker rm -f $ids >/dev/null
      }
    else
      info "compose project '$p': stop"
      run dc -p "$p" stop >&2 || {
        ids=$(docker ps -q --filter "label=com.docker.compose.project=$p")
        [ -n "$ids" ] && run docker stop $ids >/dev/null
      }
    fi
  done
  mount_containers "$root" "$projs" | while IFS='|' read -r id cname running; do
    if [ "$mode" = rm ]; then
      info "container $cname ($id): remove"; run docker rm -f "$id" >/dev/null
    elif [ "$running" = true ]; then
      info "container $cname ($id): stop"; run docker stop "$id" >/dev/null
    fi
  done
  [ -n "$projs" ] || [ -n "$(mount_containers "$root" "")" ] || info "no docker containers tied to $root"
  return 0
}

# ---------------------------------------------------------------- setup
clone_cp() {
  cp -cRp "$1" "$2" 2>/dev/null && return 0; rm -rf "$2"
  cp -Rp --reflink=auto "$1" "$2" 2>/dev/null && return 0; rm -rf "$2"
  cp -Rp "$1" "$2"
}

copy_includes() {
  local src=$1 dst=$2 pats
  if [ -f "$src/.worktreeinclude" ]; then
    pats=$(sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$src/.worktreeinclude" | grep -v -e '^$' -e '^!')
  else
    pats='.env
.env.*'
  fi
  (
    cd "$src" || exit 0
    shopt -s nullglob dotglob
    printf '%s\n' "$pats" | while IFS= read -r pat; do
      [ -n "$pat" ] || continue
      pat=${pat#/}; pat=${pat%/}
      for f in $pat; do
        [ -e "$f" ] || continue
        [ -e "$dst/$f" ] && continue
        mkdir -p "$dst/$(dirname "$f")"
        clone_cp "$f" "$dst/$f" && info "copied $f"
      done
    done
  )
}

port_busy() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

pick_port() {
  local n port taken i=0
  n=$(printf '%s' "$MAIN/$1" | cksum | awk '{print $1}')
  port=$((WORKTREECTL_PORT_BASE + (n % 500) * 10))
  taken=$(cat "$ROOT"/*/.worktree.env 2>/dev/null | sed -n 's/^WORKTREE_PORT=//p')
  while [ "$i" -lt 100 ]; do
    if ! printf '%s\n' "$taken" | grep -qx "$port" && ! port_busy "$port"; then break; fi
    port=$((port + 10)); i=$((i + 1))
  done
  printf '%s\n' "$port"
}

write_env() { # path name branch port
  local proj
  proj=$(printf '%s-%s' "$REPO_NAME" "$2" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-' '-')
  {
    echo "# generated by worktreectl. Load with: set -a; . ./.worktree.env; set +a"
    printf 'WORKTREE_NAME=%q\n' "$2"
    printf 'WORKTREE_PATH=%q\n' "$1"
    printf 'WORKTREE_MAIN=%q\n' "$MAIN"
    printf 'WORKTREE_BRANCH=%q\n' "$3"
    printf 'WORKTREE_PORT=%s\n' "$4"
    printf 'PORT=%s\n' "$4"
    printf 'COMPOSE_PROJECT_NAME=%s\n' "$proj"
  } >"$1/.worktree.env"
}

run_hook() { # hook-name worktree-path
  local h="" d st
  for d in "$2" "$MAIN"; do
    if [ -f "$d/.worktreectl/$1" ]; then h="$d/.worktreectl/$1"; break; fi
  done
  [ -n "$h" ] || return 1
  info "running hook $h"
  [ "$DRY" = 1 ] && return 0
  ( cd "$2" && load_env "$2" && if [ -x "$h" ]; then "$h"; else bash "$h"; fi ) >&2
  st=$?
  [ "$st" = 0 ] || warn "hook $1 exited with status $st"
  return 0
}

auto_setup() {
  (
    cd "$1" || exit 0
    if [ -f package.json ] && [ ! -e node_modules ]; then
      if   [ -f pnpm-lock.yaml ] && have pnpm; then info "pnpm install"; pnpm install --frozen-lockfile
      elif { [ -f bun.lock ] || [ -f bun.lockb ]; } && have bun; then info "bun install"; bun install --frozen-lockfile
      elif [ -f yarn.lock ] && have yarn; then info "yarn install"; yarn install
      elif [ -f package-lock.json ] && have npm; then info "npm ci"; npm ci
      elif have npm; then info "npm install"; npm install
      fi || warn "node dependency install failed"
    fi
    if [ -f uv.lock ] && have uv; then info "uv sync"; uv sync || warn "uv sync failed"
    elif [ -f poetry.lock ] && have poetry; then info "poetry install"; poetry install || warn "poetry install failed"
    elif [ -f requirements.txt ] && [ ! -e .venv ] && have python3; then
      info "python venv + requirements.txt"
      { python3 -m venv .venv && .venv/bin/pip install -q -r requirements.txt; } || warn "pip install failed"
    fi
    if [ -f go.mod ] && have go; then info "go mod download"; go mod download || warn "go mod download failed"; fi
  ) >&2
}

# ---------------------------------------------------------------- commands
cmd_new() {
  local name="" base="" branch="" detach=0 setup=1 copy=1 fetch=0 path port
  while [ $# -gt 0 ]; do
    case $1 in
      -b|--base)  [ $# -ge 2 ] || die "$1 needs a value"; base=$2; shift 2 ;;
      --branch)   [ $# -ge 2 ] || die "$1 needs a value"; branch=$2; shift 2 ;;
      -d|--detach) detach=1; shift ;;
      --fetch)    fetch=1; shift ;;
      --no-setup) setup=0; shift ;;
      --no-copy)  copy=0; shift ;;
      --) shift; break ;;
      -*) die "unknown option for new: $1" ;;
      *)  [ -z "$name" ] || die "unexpected argument: $1"; name=$1; shift ;;
    esac
  done
  [ -n "$name" ] || name="wt-$(date +%m%d-%H%M%S)"
  case $name in ""|.*|*[!A-Za-z0-9._-]*) die "invalid name '$name' (use letters, digits, . _ -)" ;; esac

  path=$ROOT/$name
  [ -e "$path" ] && die "$path already exists"
  if [ "$fetch" = 1 ]; then info "git fetch"; git -C "$MAIN" fetch --quiet --prune || warn "fetch failed"; fi
  [ -n "$base" ] || base=$(git rev-parse HEAD)
  git -C "$MAIN" rev-parse --verify --quiet "$base^{commit}" >/dev/null || die "unknown base ref: $base"

  mkdir -p "$ROOT"
  case "$ROOT/" in "$MAIN"/*) add_exclude "/${ROOT#"$MAIN"/}/" ;; esac
  add_exclude "/.worktree.env"

  if [ "$detach" = 1 ]; then
    git -C "$MAIN" worktree add --detach "$path" "$base" >&2 || die "git worktree add failed"
    branch="-"
  else
    [ -n "$branch" ] || branch=$WORKTREECTL_BRANCH_PREFIX$name
    if git -C "$MAIN" show-ref --verify --quiet "refs/heads/$branch"; then
      info "reusing existing branch $branch (--base ignored)"
      git -C "$MAIN" worktree add "$path" "$branch" >&2 || die "git worktree add failed"
    else
      git -C "$MAIN" worktree add --no-track -b "$branch" "$path" "$base" >&2 || die "git worktree add failed"
    fi
  fi

  [ "$copy" = 1 ] && copy_includes "$MAIN" "$path"
  port=$(pick_port "$name")
  write_env "$path" "$name" "$branch" "$port"
  if [ "$setup" = 1 ]; then run_hook setup "$path" || auto_setup "$path"; fi

  info "ready: $path (branch $branch, port $port)"
  if [ $# -gt 0 ]; then
    cd "$path" || die "cannot cd to $path"
    load_env "$path"
    exec "$@"
  fi
  printf '%s\n' "$path"
}

cmd_exec() {
  local target=""
  if [ $# -gt 0 ] && [ "$1" != "--" ]; then target=$1; shift; fi
  [ "${1:-}" = "--" ] && shift
  resolve "$target"
  cd "$T_PATH" || die "cannot cd to $T_PATH"
  load_env "$T_PATH"
  [ $# -gt 0 ] || set -- "${SHELL:-/bin/sh}"
  exec "$@"
}

cmd_path() { resolve "${1:-}"; printf '%s\n' "$T_PATH"; }

cmd_list() {
  local p b h name state ahead procs
  snapshot
  printf '%-24s %-30s %-7s %-6s %-5s %s\n' NAME BRANCH STATE AHEAD PROCS PATH
  while IFS='|' read -r p b h; do
    [ -n "$p" ] || continue
    name=${p##*/}; [ "$p" = "$MAIN" ] && name="$name (main)"
    if [ ! -d "$p" ]; then
      state=missing; ahead=-; procs=-
    else
      if [ -n "$(git -C "$p" status --porcelain 2>/dev/null | head -n 1)" ]; then state=dirty; else state=clean; fi
      ahead=$(git -C "$MAIN" rev-list --count "$BASEREF..$h" 2>/dev/null) && ahead="+$ahead" || ahead="?"
      procs=$(pids_for "$p" | wc -l | tr -d ' ')
    fi
    printf '%-24s %-30s %-7s %-6s %-5s %s\n' "$name" "$b" "$state" "$ahead" "$procs" "$p"
  done <<EOF
$(wt_table)
EOF
}

cmd_ps() {
  local lines l pid projs p id cname running
  resolve "${1:-}"
  printf 'worktree  %s  [%s]\n' "$T_PATH" "$T_BRANCH"
  snapshot
  lines=$(pids_for "$T_PATH")
  echo "processes:"
  if [ -z "$lines" ]; then
    echo "  (none)"
  else
    printf '%s\n' "$lines" | while IFS= read -r l; do
      pid=${l%%	*}
      printf '  %-7s %-22s %.110s\n' "$pid" "$(ports_of "$pid")" "${l#*	}"
    done
  fi
  echo "containers:"
  if ! docker_ok; then echo "  (docker not available)"; return 0; fi
  projs=$(compose_projects "$T_PATH" | tr '\n' ' ')
  for p in $projs; do
    docker ps -a --filter "label=com.docker.compose.project=$p" --format '{{.ID}}|{{.Names}}|{{.Status}}' |
      while IFS='|' read -r id cname running; do printf '  %-13s %-32s %-22s compose:%s\n' "$id" "$cname" "$running" "$p"; done
  done
  mount_containers "$T_PATH" "$projs" | while IFS='|' read -r id cname running; do
    printf '  %-13s %-32s %-22s bind-mount\n' "$id" "$cname" "running=$running"
  done
  [ -n "$projs" ] || [ -n "$(mount_containers "$T_PATH" "")" ] || echo "  (none)"
}

cmd_stop() {
  local target=""
  VOLFLAG=""
  while [ $# -gt 0 ]; do
    case $1 in
      -n|--dry-run) DRY=1; shift ;;
      -*) die "unknown option for stop: $1" ;;
      *) target=$1; shift ;;
    esac
  done
  resolve "$target"; not_main
  stop_all "$T_PATH" stop
}

cmd_rm() {
  local target="" force=0 keep=0 inside=0 dirty rmflags=""
  VOLFLAG=""
  while [ $# -gt 0 ]; do
    case $1 in
      -n|--dry-run) DRY=1; shift ;;
      -f|--force) force=1; shift ;;
      --keep-branch) keep=1; shift ;;
      --volumes) VOLFLAG="--volumes"; shift ;;
      -*) die "unknown option for rm: $1" ;;
      *) target=$1; shift ;;
    esac
  done
  resolve "$target"; not_main

  if [ "$force" = 0 ] && [ -d "$T_PATH" ]; then
    dirty=$(git -C "$T_PATH" status --porcelain 2>/dev/null | head -n 5)
    if [ -n "$dirty" ]; then
      printf '%s\n' "$dirty" | sed 's/^/    /' >&2
      die "$T_PATH has uncommitted changes; commit or stash them, or pass --force"
    fi
    if [ "$T_BRANCH" = "-" ] &&
       ! git -C "$MAIN" merge-base --is-ancestor "$T_HEAD" "$BASEREF" 2>/dev/null &&
       [ -z "$(git -C "$MAIN" branch --contains "$T_HEAD" 2>/dev/null)" ]; then
      die "detached HEAD has commits that are on no branch; save them with: git -C '$T_PATH' switch -c <branch>   (or pass --force)"
    fi
  fi

  case "$(phys "$PWD")/" in "$(phys "$T_PATH")"/*) inside=1; cd "$MAIN" || exit 1 ;; esac

  [ -d "$T_PATH" ] && { run_hook teardown "$T_PATH" || true; }
  stop_all "$T_PATH" rm

  [ "$force" = 1 ] && rmflags="--force --force"
  info "removing worktree $T_PATH"
  if [ -d "$T_PATH" ]; then
    run git -C "$MAIN" worktree remove $rmflags "$T_PATH" || die "git worktree remove failed (use --force to override)"
  fi

  if [ "$T_BRANCH" != "-" ] && [ "$keep" = 0 ]; then
    if [ "$force" = 1 ]; then
      run git -C "$MAIN" branch -D "$T_BRANCH" >&2
    elif [ "$DRY" = 1 ]; then
      info "[dry-run] git branch -d $T_BRANCH (kept if not merged)"
    elif git -C "$MAIN" branch -d "$T_BRANCH" >/dev/null 2>&1; then
      info "deleted branch $T_BRANCH"
    else
      warn "kept branch $T_BRANCH: not merged yet (push it / open a PR, or delete with: git branch -D $T_BRANCH)"
    fi
  fi
  run git -C "$MAIN" worktree prune
  [ "$inside" = 1 ] && info "your shell was inside the removed worktree; cd \"$MAIN\""
  return 0
}

cmd_gc() {
  local all=0 p b h n=0
  while [ $# -gt 0 ]; do
    case $1 in
      -n|--dry-run) DRY=1; shift ;;
      --all) all=1; shift ;;
      *) die "unknown option for gc: $1" ;;
    esac
  done
  snapshot
  while IFS='|' read -r p b h; do
    [ -n "$p" ] && [ "$p" != "$MAIN" ] || continue
    if [ "$all" = 0 ]; then case "$p/" in "$ROOT"/*) ;; *) continue ;; esac; fi
    [ -d "$p" ] || continue
    if [ -n "$(git -C "$p" status --porcelain 2>/dev/null | head -n 1)" ]; then info "keep ${p##*/}: uncommitted changes"; continue; fi
    if ! git -C "$MAIN" merge-base --is-ancestor "$h" "$BASEREF" 2>/dev/null; then info "keep ${p##*/}: commits not in $BASEREF"; continue; fi
    if [ -n "$(pids_for "$p")" ]; then info "keep ${p##*/}: processes still running (see: $PROG ps ${p##*/})"; continue; fi
    ( cmd_rm "$p" ) && n=$((n + 1))
  done <<EOF
$(wt_table)
EOF
  run git -C "$MAIN" worktree prune
  info "gc: $n worktree(s) removed"
}

# ---------------------------------------------------------------- main
main() {
  local cmd=${1:-}
  [ -n "$cmd" ] || { usage >&2; exit 2; }
  shift
  case $cmd in
    -h|--help|help) usage; exit 0 ;;
    -V|--version|version) echo "$PROG $VERSION"; exit 0 ;;
  esac
  have git || die "git not found"
  init_repo
  case $cmd in
    new|start)   cmd_new "$@" ;;
    exec|enter)  cmd_exec "$@" ;;
    path)        cmd_path "$@" ;;
    list|ls)     cmd_list "$@" ;;
    ps|status)   cmd_ps "$@" ;;
    stop)        cmd_stop "$@" ;;
    rm|remove|done) cmd_rm "$@" ;;
    gc)          cmd_gc "$@" ;;
    *) die "unknown command: $cmd (try: $PROG help)" ;;
  esac
}

main "$@"
