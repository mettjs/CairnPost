package console

import "fmt"

// size says a number of bytes as a person reads it.
func size(n int) string {
	switch {
	case n == 1:
		return "1 byte"
	case n < 1000:
		return fmt.Sprintf("%d bytes", n)
	case n < 999950: // above it, a tenth of a KB rounds up to 1000.0
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1000*1000))
}
