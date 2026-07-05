//go:build linux

// item.go — модель записи истории. Запись — это ЛИБО текст, ЛИБО картинка
// (дискриминированное объединение по kind). Мутации истории (дедуп, лимиты)
// тоже здесь, чтобы daemon.go оставался про сокет/бэкенд.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"

	"github.com/gotk3/gotk3/gdk"
)

type itemKind int

const (
	kindText  itemKind = iota // текстовая запись
	kindImage                 // картинка
)

// clipItem — одна запись истории буфера (только в памяти).
type clipItem struct {
	kind itemKind
	text string      // kindText: полный текст (вставляется целиком)
	png  []byte      // kindImage: канонические PNG-байты (реотдача в буфер + дедуп)
	pix  *gdk.Pixbuf // kindImage: декодированный полноразмерный pixbuf (SetImage + cover-рендер)
	key  string      // идентичность записи для дедупа и метки self-set
}

// maxImageBytes — байтовый бюджет на картинки в истории. Текстовые записи считаются
// только по числу (maxHistory), картинки могут быть тяжёлыми (скриншоты), поэтому
// сверх лимита вытесняем старейшие картинки, не трогая текст.
const maxImageBytes = 64 << 20 // 64 MiB

// pixbufFromPNG декодирует PNG-байты в pixbuf через gdk-pixbuf loader (PNG-лоадер
// идёт с GTK3, внешних зависимостей нет). Вызывать из GTK-потока.
func pixbufFromPNG(png []byte) (*gdk.Pixbuf, error) {
	ld, err := gdk.PixbufLoaderNew()
	if err != nil {
		return nil, err
	}
	pix, err := ld.WriteAndReturnPixbuf(png)
	ld.Close() // best-effort: pixbuf уже получен
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

// addItem кладёт запись наверх истории: дедуп по key (старая позиция убирается),
// затем лимиты (число записей + байтовый бюджет картинок). Только в памяти.
// Вызывать только из главного GTK-потока.
func addItem(it *clipItem) {
	for i, e := range history { // дедуп: убрать старую позицию такой же записи
		if e.key == it.key {
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	history = append([]*clipItem{it}, history...) // свежее — сверху
	enforceLimits()
	log.Printf("history: %d записей", len(history))
}

// enforceLimits обрезает историю по числу записей и по байтовому бюджету картинок.
func enforceLimits() {
	if len(history) > maxHistory {
		history = history[:maxHistory]
	}
	total := 0
	for _, e := range history {
		total += len(e.png)
	}
	for total > maxImageBytes { // вытесняем старейшие картинки, пока не влезем в бюджет
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
