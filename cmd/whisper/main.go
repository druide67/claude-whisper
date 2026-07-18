// Command whisper is the single multi-command binary for the whisper IPC bus.
// It is invoked three ways, all pointing at this one executable:
//   - as a CLI: whisper send|broadcast|list|clean|init|rehome|doctor …
//   - as the UserPromptSubmit hook: whisper check-inbox
//   - as an SSH forced-command: whisper spool-server <peer-id>
//
// Busybox-style compatibility: a symlink named whisper-<cmd> (whisper-send,
// whisper-list, …) dispatches on argv[0], so the historical per-command names
// keep working — sessions and docs that learned them don't break.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/druide67/claude-whisper/internal/cmd"
)

func main() { os.Exit(run(os.Args[0], os.Args[1:])) }

func run(argv0 string, args []string) int {
	if base := filepath.Base(argv0); strings.HasPrefix(base, "whisper-") {
		args = append([]string{strings.TrimPrefix(base, "whisper-")}, args...)
	}
	if len(args) == 0 {
		usage()
		return 1
	}
	name, rest := args[0], args[1:]
	fn, ok := map[string]func([]string) int{
		"send":         cmd.Send,
		"broadcast":    cmd.Broadcast,
		"list":         cmd.List,
		"clean":        cmd.Clean,
		"init":         cmd.Init,
		"rehome":       cmd.Rehome,
		"check-inbox":  cmd.CheckInbox,
		"spool-server": cmd.SpoolServer,
		"doctor":       cmd.Doctor,
	}[name]
	if !ok {
		switch name {
		case "-h", "--help", "help":
			usage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "whisper: unknown command %q\n", name)
		usage()
		return 1
	}
	return fn(rest)
}

func usage() {
	fmt.Fprint(os.Stderr, `whisper — inter-instance IPC bus

Usage: whisper <command> [args]

Commands:
  send [-t thread] [-f from] [-r reply-to] [-s session|'*'] [-p normal|urgent] [-F] <peer> <message>
  broadcast [-t thread] [-f from] [-p normal|urgent] <message>
  list [--sessions]
  clean [days]
  init <peer-id> [project-dir] | <peer-id> --transport <type> [--ssh-alias X] [--key-path Y]
  rehome <wrong-peer> <correct-peer> [--yes]
  doctor [--fix] [--yes] [--list-orphans]
  check-inbox            (UserPromptSubmit hook; reads event JSON on stdin)
  spool-server <peer-id> (SSH forced-command target)
`)
}
