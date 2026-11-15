// Package page is the one rule for how many records a page of a list
// holds, whatever the list.
package page

const (
	// Default is a page's size when none is asked for.
	Default = 50
	// Most is the largest page the API gives.
	Most = 200
	// Hard is the largest page anything inside the server reads.
	Hard = 1000
)

// Size is how many records to read for what was asked: Default for
// none, and never more than Hard.
func Size(asked int) int {
	if asked <= 0 {
		return Default
	}
	return min(asked, Hard)
}
