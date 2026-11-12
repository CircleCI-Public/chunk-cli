package gitutil

import "strings"

// DirtySubmodules returns the paths of submodules at dir whose own working
// tree holds changes: modified or untracked files inside the submodule.
//
// A snapshot records a submodule only as the commit it is checked out at, so
// such changes are not in the tree SHA. A submodule checked out at a different
// commit than the superproject records is not listed: the snapshot does carry
// that commit.
//
// It reads `git status --porcelain=v2`, whose submodule field is "N..." for an
// ordinary path and "S<c><m><u>" for a submodule, with m and u set when its
// content is modified or has untracked files.
func DirtySubmodules(dir string) ([]string, error) {
	status, err := gitOut(dir, "status", "--porcelain=v2", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(status, "\x00")
	for i := 0; i < len(fields); i++ {
		// Entries: "1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>",
		// "2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <Xscore> <path>" followed by
		// "<origPath>" as its own field, and
		// "u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>".
		// "? <path>" and "! <path>" carry no submodule field.
		f := fields[i]
		var parts []string
		switch {
		case strings.HasPrefix(f, "1 "):
			parts = strings.SplitN(f, " ", 9)
		case strings.HasPrefix(f, "2 "):
			parts = strings.SplitN(f, " ", 10)
			i++
		case strings.HasPrefix(f, "u "):
			parts = strings.SplitN(f, " ", 11)
		default:
			continue
		}
		if len(parts) < 3 {
			continue
		}
		sub := parts[2]
		if len(sub) == 4 && sub[0] == 'S' && (sub[2] == 'M' || sub[3] == 'U') {
			paths = append(paths, parts[len(parts)-1])
		}
	}
	return paths, nil
}
