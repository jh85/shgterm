#!/bin/sh
# Minimal USI engine used by unit tests. Responds to usi/isready/go/quit;
# echoes setoption lines to stderr so the test can assert on them.
while IFS= read -r line; do
  case "$line" in
    usi)
      echo "id name FakeEngine"
      echo "id author test"
      echo "option name USI_Hash type spin default 16 min 1 max 33554432"
      echo "option name USI_Ponder type check default false"
      echo "option name MultiPV type spin default 1 min 1 max 500"
      echo "usiok"
      ;;
    isready)
      echo "readyok"
      ;;
    usinewgame) ;;
    "position "*) ;;
    "setoption "*)
      echo "$line" >&2
      ;;
    "go "*)
      # Emit a couple of info lines and a fixed bestmove.
      echo "info depth 1 score cp 42 nodes 100 nps 1000 pv 7g7f"
      echo "info depth 2 score cp 50 nodes 500 nps 5000 pv 7g7f 3c3d"
      echo "bestmove 7g7f ponder 3c3d"
      ;;
    stop)
      echo "bestmove 7g7f"
      ;;
    gameover*) ;;
    quit)
      exit 0
      ;;
  esac
done
