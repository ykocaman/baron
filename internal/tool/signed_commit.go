package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// signedCodes are git's %G? signature status codes that count as a valid
// signature: G (good, trusted key) and U (good signature, key not marked
// trusted locally — still a real signature, just a local trust-store gap).
// Only signed vs. unsigned is distinguished; anything else (B bad, X/Y
// expired, E can't check, N none) counts as unsigned.
var signedCodes = map[string]bool{"G": true, "U": true}

// UnsignedCommits returns the SHAs in base..HEAD without a valid signature,
// sorted for deterministic output.
func UnsignedCommits(ctx context.Context, runner Runner, dir, base string) ([]string, error) {
	res, err := runner.Run(ctx, "git", []string{"log", "--pretty=%H %G?", base + "..HEAD"}, Options{Dir: dir})
	if err != nil {
		return nil, fmt.Errorf("git log --pretty=%%H %%G?: %w", err)
	}
	var unsigned []string
	for line := range strings.SplitSeq(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		sha, code, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if !signedCodes[code] {
			unsigned = append(unsigned, sha)
		}
	}
	sort.Strings(unsigned)
	return unsigned, nil
}
