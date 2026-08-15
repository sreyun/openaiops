//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// escapeAgentCgroup moves pid out of the Agent's systemd unit cgroup into the
// root cgroup. Children forked afterwards inherit the escaped cgroup, so a
// later systemctl restart of aiops-agent (KillMode=mixed/control-group) cannot
// SIGKILL Java/xjar started from the remote terminal.
//
// Setsid only leaves the session; it does not change cgroup membership.
func escapeAgentCgroup(pid int) bool {
	if pid <= 1 {
		return false
	}
	line := []byte(strconv.Itoa(pid))
	for _, path := range []string{
		"/sys/fs/cgroup/cgroup.procs",
		"/sys/fs/cgroup/systemd/cgroup.procs",
		"/sys/fs/cgroup/name=systemd/cgroup.procs",
	} {
		if err := os.WriteFile(path, line, 0); err == nil {
			return true
		}
	}
	return false
}

// escapeAgentCgroupTree moves pid and every current descendant. A shell often
// forks (nsenter → bash) before we get scheduled, and cgroup v2 does not move
// children when the parent is moved.
func escapeAgentCgroupTree(root int) {
	if root <= 1 {
		return
	}
	for i := 0; i < 5; i++ {
		seen := map[int]bool{}
		var walk func(int)
		walk = func(pid int) {
			if pid <= 1 || seen[pid] {
				return
			}
			seen[pid] = true
			for _, c := range listChildPIDs(pid) {
				walk(c)
			}
			escapeAgentCgroup(pid)
		}
		walk(root)
		if i < 4 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func listChildPIDs(parent int) []int {
	if parent <= 1 {
		return nil
	}
	if raw, err := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + strconv.Itoa(parent) + "/children"); err == nil {
		var out []int
		for _, f := range strings.Fields(string(raw)) {
			if n, err := strconv.Atoi(f); err == nil && n > 1 {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return listChildPIDsScan(parent)
}

func listChildPIDsScan(parent int) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	want := strconv.Itoa(parent)
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(st)
		i := strings.LastIndex(s, ")")
		if i < 0 {
			continue
		}
		fields := strings.Fields(s[i+1:])
		if len(fields) < 2 {
			continue
		}
		if fields[1] == want {
			out = append(out, pid)
		}
	}
	return out
}
