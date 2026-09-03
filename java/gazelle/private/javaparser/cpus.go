package javaparser

import (
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

func availableCPUs() int {
	visible := max(1, runtime.NumCPU())
	if runtime.GOOS != "linux" {
		return visible
	}
	return limitCPUs(visible, os.ReadFile)
}

func limitCPUs(visible int, readFile func(string) ([]byte, error)) int {
	cpus := max(1, visible)
	cgroups, err := readFile("/proc/self/cgroup")
	if err != nil {
		return cpus
	}
	mounts, err := readFile("/proc/self/mountinfo")
	if err != nil {
		return cpus
	}
	groups := map[string]string{}
	for _, line := range strings.Split(string(cgroups), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 {
			for _, controller := range strings.Split(fields[1], ",") {
				groups[controller] = fields[2]
			}
		}
	}
	for _, line := range strings.Split(string(mounts), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, filesystem := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 5 || len(filesystem) < 3 {
			continue
		}
		v2 := filesystem[0] == "cgroup2"
		controller := ""
		if !v2 {
			if filesystem[0] != "cgroup" || !strings.Contains(","+filesystem[2]+",", ",cpu,") {
				continue
			}
			controller = "cpu"
		}
		group, ok := groups[controller]
		if !ok {
			continue
		}
		root, mount := unescapeMountPath(fields[3]), unescapeMountPath(fields[4])
		relative := ""
		if group != root {
			relative, ok = strings.CutPrefix(group, strings.TrimSuffix(root, "/")+"/")
			if !ok {
				continue
			}
		}
		if strings.Contains("/"+relative+"/", "/../") {
			continue
		}
		// A leaf can be unlimited while its parent still restricts the process.
		for directory := path.Join(mount, relative); ; directory = path.Dir(directory) {
			if quota := cgroupQuota(directory, v2, readFile); quota > 0 {
				cpus = int(min(int64(cpus), quota))
			}
			if directory == mount || directory == "/" {
				break
			}
		}
	}
	return cpus
}

func cgroupQuota(directory string, v2 bool, readFile func(string) ([]byte, error)) int64 {
	var values []string
	if v2 {
		data, err := readFile(path.Join(directory, "cpu.max"))
		if err != nil {
			return 0
		}
		values = strings.Fields(string(data))
	} else {
		quota, err := readFile(path.Join(directory, "cpu.cfs_quota_us"))
		if err != nil {
			return 0
		}
		period, err := readFile(path.Join(directory, "cpu.cfs_period_us"))
		if err != nil {
			return 0
		}
		values = []string{strings.TrimSpace(string(quota)), strings.TrimSpace(string(period))}
	}
	if len(values) != 2 {
		return 0
	}
	quota, quotaErr := strconv.ParseInt(values[0], 10, 64)
	period, periodErr := strconv.ParseInt(values[1], 10, 64)
	if quotaErr != nil || periodErr != nil || quota <= 0 || period <= 0 {
		return 0
	}
	return (quota-1)/period + 1
}

func unescapeMountPath(value string) string {
	return strings.NewReplacer(
		"\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\",
	).Replace(value)
}
