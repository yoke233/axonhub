package main

import (
	"context"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/looplj/axonhub/internal/log"
)

// configureMemoryLimit sets the Go runtime soft memory limit (GOMEMLIMIT)
// based on the cgroup quota when the user has not provided one. With a soft
// limit the GC scavenger is far more aggressive about returning memory to the
// OS, which keeps container RSS in line with the quota instead of trailing the
// historical peak.
func configureMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}

	limit, ok := readCgroupMemoryLimit()
	if !ok || limit <= 0 {
		return
	}

	// Headroom for non-heap allocations (stacks, runtime, mmap'd buffers).
	soft := int64(float64(limit) * 0.9)
	debug.SetMemoryLimit(soft)

	log.Info(context.Background(), "applied GOMEMLIMIT from cgroup",
		log.Int64("cgroup_limit_bytes", limit),
		log.Int64("gomemlimit_bytes", soft),
	)
}

// readCgroupMemoryLimit returns the memory limit reported by cgroup v2 then
// cgroup v1. Returns ok=false when the limit is "max" / unset / unreadable.
func readCgroupMemoryLimit() (int64, bool) {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",                   // cgroup v2
		"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		s := strings.TrimSpace(string(raw))
		if s == "" || s == "max" {
			return 0, false
		}

		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}

		// cgroup v1 reports a near-int64-max sentinel when unlimited.
		if n <= 0 || n > 1<<62 {
			return 0, false
		}

		return n, true
	}

	return 0, false
}
