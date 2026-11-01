package main

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is one dispatched server-sent event.
type sseEvent struct {
	Name string // "message" when the stream sent no `event:` field
	Data string
}

// readSSE parses a text/event-stream until EOF or until fn returns false.
// Comment lines (": ping") are ignored.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var name string
	var data []string
	dispatch := func() bool {
		if name == "" && len(data) == 0 {
			return true
		}
		ev := sseEvent{Name: name, Data: strings.Join(data, "\n")}
		if ev.Name == "" {
			ev.Name = "message"
		}
		name, data = "", nil
		return fn(ev)
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if !dispatch() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name = value
			case "data":
				data = append(data, value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	dispatch()
	return nil
}
