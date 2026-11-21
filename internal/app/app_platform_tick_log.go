package app

// platformTickLog keeps a scheduler that fails the same way on every tick from logging that failure
// on every tick. Before the application platform is initialized each tick reports the control
// pause, several times a minute; the state is logged when it starts, when the error changes and
// when ticks succeed again. Errors are compared by their text, which is what an operator reads.
type platformTickLog struct {
	failing bool
	last    string
}

// report returns the line to log for a tick's result, if any.
func (l *platformTickLog) report(err error) (string, bool) {
	if err == nil {
		if !l.failing {
			return "", false
		}
		l.failing, l.last = false, ""
		return "app-platform scheduler tick: recovered", true
	}
	text := err.Error()
	if l.failing && text == l.last {
		return "", false
	}
	l.failing, l.last = true, text
	return "app-platform scheduler tick: " + text + " (repeats are not logged until the state changes)", true
}
