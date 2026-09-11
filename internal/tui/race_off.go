//go:build !race

package tui

// raceEnabled is true when the binary was built with -race. See its use in
// deadlock_test.go for why this exists.
const raceEnabled = false
