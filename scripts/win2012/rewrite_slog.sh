#!/usr/bin/env bash
# rewrite_slog.sh 鈥?adapt agent sources for golang.org/x/exp/slog (Go 1.20).
# Usage: rewrite_slog.sh <agent-source-dir>
set -eu
DIR="${1:?agent source dir required}"

# 1) stdlib import 鈫?x/exp
find "$DIR" -type f -name '*.go' -print0 \
  | xargs -0 sed -i 's|"log/slog"|"golang.org/x/exp/slog"|g'

# 2) NewTextHandler(w, opts) 鈫?opts.NewTextHandler(w) inside the helper only.
#    x/exp/slog (pre-graduation) uses the method form; stdlib uses the 2-arg form.
if [[ -f "$DIR/slog_handler.go" ]]; then
  sed -i \
    's|return slog\.NewTextHandler(w, \&slog\.HandlerOptions{Level: slog\.LevelInfo})|return (\&slog.HandlerOptions{Level: slog.LevelInfo}).NewTextHandler(w)|' \
    "$DIR/slog_handler.go"
fi
