package web

import "strings"

// The sidebar's entries: an entry is current on its own page, and marked
// as the section one is in on the pages below it.

type navEntry struct {
	Href, Label, Icon string
	Current, Within   bool
}

// instrumentOf is what the instrument template needs: the source's name
// for its labels, and whether it is the smaller one of a trial.
type instrumentOf struct {
	Name  string
	Small bool
}

func instrument(name string, small bool) instrumentOf { return instrumentOf{name, small} }

func navItem(path, href, label, icon string) navEntry {
	e := navEntry{Href: href, Label: label, Icon: icon, Current: path == href}
	if !e.Current && href != "/" {
		e.Within = strings.HasPrefix(path, href+"/")
	}
	return e
}
