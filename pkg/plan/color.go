package plan

// Color turns on ANSI colours in Show and the helpers below. The CLI sets it
// for a terminal, unless NO_COLOR or --no-color says otherwise; it is off for
// files and pipes, so saved output stays plain.
var Color bool

func paint(code, s string) string {
	if !Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Bold(s string) string   { return paint("1", s) }
func Dim(s string) string    { return paint("2", s) }
func Green(s string) string  { return paint("32", s) }
func Yellow(s string) string { return paint("33", s) }
func Red(s string) string    { return paint("31", s) }
func Cyan(s string) string   { return paint("36", s) }

// opColor colours a diff line by its operation.
func opColor(op, s string) string {
	switch op {
	case "+":
		return Green(s)
	case "-":
		return Red(s)
	case "~":
		return Yellow(s)
	}
	return s
}
