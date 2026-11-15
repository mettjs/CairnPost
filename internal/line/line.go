// Package line says what a header's value must be to be written on
// lines a mail server takes.
package line

// MaxWord is the longest run without a space a value may have when it
// is plain ASCII: a mail server takes lines of 998 bytes, and a line can
// be broken only at a space. Other text is written in an encoded form
// that breaks anywhere.
const MaxWord = 900

// Unbroken reports whether a value written as it is would make a line
// no mail server takes.
func Unbroken(value string) bool {
	run := 0
	for i := range len(value) {
		switch c := value[i]; {
		case c < 0x20 || c > 0x7e:
			return false // written encoded
		case c == ' ':
			run = 0
		default:
			if run++; run > MaxWord {
				return true
			}
		}
	}
	return false
}
