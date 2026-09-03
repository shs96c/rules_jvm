package javaparser

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCPULimitIncludesAncestorQuotas(t *testing.T) {
	for _, test := range []struct {
		name    string
		visible int
		parent  string
		child   string
		want    int
	}{
		{"inherited quota", 32, "800000 100000", "max 100000", 8},
		{"tighter child", 32, "800000 100000", "400000 100000", 4},
		{"affinity is smaller", 2, "800000 100000", "max 100000", 2},
		{"fractional quota", 32, "150000 100000", "max 100000", 2},
		{"unlimited", 32, "max 100000", "max 100000", 32},
		{"malformed child", 32, "800000 100000", "invalid", 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := cpuFiles(map[string]string{
				"proc/self/cgroup":                 "0::/init.scope\n",
				"proc/self/mountinfo":              "1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n",
				"sys/fs/cgroup/cpu.max":            test.parent,
				"sys/fs/cgroup/init.scope/cpu.max": test.child,
			})
			if got := limitCPUs(test.visible, files); got != test.want {
				t.Fatalf("CPU limit = %d, want %d", got, test.want)
			}
		})
	}
}

func TestCPULimitHandlesV1AndMountedSubtrees(t *testing.T) {
	files := cpuFiles(map[string]string{
		"proc/self/cgroup":                          "5:cpu,cpuacct:/containers/task/child\n",
		"proc/self/mountinfo":                       "1 0 0:1 /containers/task /sys/fs/cgroup/cpu rw - cgroup cgroup rw,cpu,cpuacct\n",
		"sys/fs/cgroup/cpu/cpu.cfs_quota_us":        "300000",
		"sys/fs/cgroup/cpu/cpu.cfs_period_us":       "100000",
		"sys/fs/cgroup/cpu/child/cpu.cfs_quota_us":  "-1",
		"sys/fs/cgroup/cpu/child/cpu.cfs_period_us": "100000",
	})
	if got := limitCPUs(32, files); got != 3 {
		t.Fatalf("CPU limit = %d, want 3", got)
	}
}

func TestCPULimitFallsBackWhenCgroupsAreUnavailable(t *testing.T) {
	for _, visible := range []int{0, 1, 6} {
		if got := limitCPUs(visible, cpuFiles(nil)); got != max(1, visible) {
			t.Fatalf("CPU limit = %d for %d visible CPUs", got, visible)
		}
	}
}

func cpuFiles(contents map[string]string) func(string) ([]byte, error) {
	files := fstest.MapFS{}
	for name, content := range contents {
		files[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return func(name string) ([]byte, error) {
		return fs.ReadFile(files, strings.TrimPrefix(name, "/"))
	}
}
