# Regenerate docs/images/demo.gif:
#   go build -o env-starter ./cmd/env-starter
#   docker pull postgres:16-alpine (pre-cache so the recording isn't slowed by the pull; Docker must be running)
#   vhs docs/examples/demo.tape    (run from the repo root; needs local network for ttyd)
#   ./env-starter shutdown         (stop the daemon + container spawned while recording)

Output docs/images/demo.gif

Require ./env-starter

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 750
Set Padding 20
Set Theme "Dracula"

Hide
Set TypingSpeed 1ms
Type "./env-starter shutdown 2>/dev/null; clear"
Enter
Sleep 500ms
Type "./env-starter allow --config docs/examples/demo.yaml --yes >/dev/null; clear"
Enter
Sleep 500ms
Show

Set TypingSpeed 60ms
Type "./env-starter --config docs/examples/demo.yaml"
Sleep 400ms
Enter
Sleep 1500ms
Type "s"
Sleep 5s
