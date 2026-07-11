//go:build linux

// item.go — the history entry model. An entry is EITHER text OR an image
// (a discriminated union by kind). History mutations (dedup, limits) live
// here too, so daemon.go stays about the socket/backend.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"

	"github.com/gotk3/gotk3/gdk"
)

type itemKind int

const (
	kindText  itemKind = iota // text entry
	kindImage                 // image
)

// clipItem — a single clipboard history entry (in memory only).
type clipItem struct {
	kind itemKind
	text string      // kindText: the full text (pasted in full)
	png  []byte      // kindImage: canonical PNG bytes (re-serving to the clipboard + dedup)
	pix  *gdk.Pixbuf // kindImage: decoded full-size pixbuf (SetImage + cover-render)
	key  string      // entry identity for dedup and the self-set marker
}

// maxImageBytes — the byte budget for images in history. Text entries are counted
// only by count (maxHistory); images can be heavy (screenshots), so beyond the limit
// we evict the oldest images without touching text.
const maxImageBytes = 64 << 20 // 64 MiB

// pixbufFromPNG decodes PNG bytes into a pixbuf via the gdk-pixbuf loader (the PNG
// loader ships with GTK3, no external dependencies). Call from the GTK thread.
func pixbufFromPNG(png []byte) (*gdk.Pixbuf, error) {
	ld, err := gdk.PixbufLoaderNew()
	if err != nil {
		return nil, err
	}
	pix, err := ld.WriteAndReturnPixbuf(png)
	ld.Close() // best-effort: the pixbuf is already obtained
	if err != nil {
		return nil, err
	}
	return pix, nil
}

func textKey(s string) string { return "t:" + s }

func imageKey(png []byte) string {
	h := sha256.Sum256(png)
	return "i:" + hex.EncodeToString(h[:])
}

// addItem puts an entry at the top of history: dedup by key (the old position is
// removed), then limits (entry count + image byte budget). In memory only.
// Call only from the main GTK thread.
func addItem(it *clipItem) {
	for i, e := range history { // dedup: remove the old position of the same entry
		if e.key == it.key {
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	history = append([]*clipItem{it}, history...) // newest on top
	enforceLimits()
	log.Printf("history: %d entries", len(history))
}

// enforceLimits trims history by entry count and by the image byte budget.
func enforceLimits() {
	if len(history) > maxHistory {
		history = history[:maxHistory]
	}
	total := 0
	for _, e := range history {
		total += len(e.png)
	}
	for total > maxImageBytes { // evict the oldest images until we fit the budget
		idx := -1
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].kind == kindImage {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		total -= len(history[idx].png)
		history = append(history[:idx], history[idx+1:]...)
	}
}
