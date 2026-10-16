package web

import "strings"

// The sidebar's entries: an entry is current on its own page, and marked
// as the section one is in on the pages below it.

type navEntry struct {
	Href, Label, Icon string
	Current, Within   bool
}

func navItem(path, href, label, icon string) navEntry {
	e := navEntry{Href: href, Label: label, Icon: icon, Current: path == href}
	if !e.Current && href != "/" {
		e.Within = strings.HasPrefix(path, href+"/")
	}
	return e
}
