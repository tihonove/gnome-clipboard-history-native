# CLAUDE.md

Указания для работы над этим репозиторием. Подробности устройства — в
[ARCHITECTURE.md](./ARCHITECTURE.md).

## Что это

Нативная история буфера обмена для **GNOME (X11 + базовый Wayland)** — аналог
`Win+V`. Резидентный GTK-демон на Go: по горячей клавише (`Super+Ctrl+V`)
показывает попап-список записей (тема Yaru), стрелки/PageUp/PageDown/Home/End
двигают выбор, `Enter` вставляет выбранное в активное окно, `Escape` закрывает.

Один бинарник (`cmd/clipmgr`), два бэкенда, выбор в рантайме (`isWayland()` в
`cmd/clipmgr/wayland.go`):
- **X11** (`x11.go`) — полноценный: попап у курсора, xgb-граб, XTEST-вставка,
  реальный захват истории.
- **Wayland** (`wayland.go` + `internal/uinput`) — попап по центру, штатные GTK-сигналы,
  вставка через `/dev/uinput` (`Shift+Insert`). История — через **XWayland-мост**:
  штатный wl-путь чужой буфер в фоне не видит (нет `data-control`), но mutter зеркалит
  буфер в X11 CLIPBOARD, и мы ловим XFIXES-уведомления по XWayland и читаем селекшн
  in-process по xgb (без внешних утилит — как CopyQ).

## Окружение и требования

- **X11**: XTEST-инжект и override-redirect окна.
- **Wayland (GNOME)**: попап — обычный toplevel (получает фокус), вставка — через
  `/dev/uinput` (нужен доступ на запись; иначе udev-правило). Переключение раскладки
  настраивать через **GNOME Tweaks** (не Settings), иначе модификаторы «съедаются» и
  хоткей/вставка ломаются на 2-й раскладке.
- **GNOME (mutter)**: горячая клавиша вешается через сам GNOME (gsettings custom
  keybinding), т.к. mutter держит `Super` и приложение не может перехватить его
  через XGrabKey. Работает и на X11, и на Wayland.
- Сборка: **Go 1.23+**, **cgo**, `libgtk-3-dev`. Рантайм: GTK3; на X11 — X-сервер с
  XTEST, на Wayland — доступ к `/dev/uinput`.

## Сборка и dev-цикл

```sh
go build -o clipmgr ./cmd/clipmgr

# перезапустить демона после пересборки (типичный цикл разработки):
pkill -x clipmgr; sleep 0.3; rm -f "$XDG_RUNTIME_DIR/clipmgr.sock"
setsid ./clipmgr >>/tmp/clipmgr.log 2>&1 &   # лог демона тут
```

- Клиент/«звонок» (то, что делает GNOME-хоткей): `./clipmgr --show`.
- **Строго один инстанс** — демон проверяет сокет `$XDG_RUNTIME_DIR/clipmgr.sock`
  перед стартом. Перед запуском нового всегда `pkill` старый.

## Как проверять (автотестов нет)

Само-тест без физической клавиатуры — синтетические клавиши доходят, т.к. ввод
идёт через xgb-grab на root:

```sh
./clipmgr --show; sleep 0.4
xdotool key --clearmodifiers Down; xdotool key --clearmodifiers Return
xsel -ob        # проверить, что в буфер попал текст выбранной записи
```

Что важно проверять физически (синтетикой не покрыть надёжно): реальную вставку
в **консоль** (kitty → Ctrl+Shift+V) и в **обе раскладки** (баг с раскладкой уже
чинён remap'ом keycode).

## Структура

Стандартная Go-раскладка: бинарник в `cmd/`, приватные пакеты в `internal/`.
Модуль — `github.com/tihonove/gnome-clipboard-history-native`.

- `cmd/clipmgr/` — package main, разрезан по файлам:
  - `main.go` — точка входа, диспатч флагов, `version`;
  - `client.go` — «звонок» `--show` (его дёргает GNOME-хоткей);
  - `install.go` — `--install`/`--uninstall`, gsettings-helpers;
  - `daemon.go` — резидентная часть: сокет, инициализация бэкенда, слушалка
    буфера, история, `setClipboard`;
  - `popup.go` — общее для бэкендов: CSS, конструктор содержимого `buildPopupBox()`;
  - `x11.go` — **X11-бэкенд**: попап у курсора, grab/poll, XTEST-вставка через
    запасной keycode, позиционирование;
  - `wayland.go` — **Wayland-бэкенд**: `isWayland()`, `showPopupWayland()`,
    `finishWayland()`, XWayland-мост истории.
- `internal/uinput/` — виртуальная клавиатура через `/dev/uinput` (вставка на
  Wayland): `Init()`, `Close()`, `InjectPaste()`.
- `.golangci.yml` — конфиг golangci-lint (гоняется в CI; намеренные
  fire-and-forget вызовы исключены точечно — см. комментарии в конфиге).

## Инварианты и грабли (НЕ регрессировать)

- **Хоткей — только через GNOME** (gsettings), не XGrabKey. mutter держит Super.
  Бинд — `Super+Ctrl+V` (`hotkeyBinding`). Работает и на X11, и на Wayland.
- **Два бэкенда, выбор в рантайме** через `isWayland()`. Ветвление ровно в двух швах:
  инициализация в `runDaemon` и диспатч `show`. X11-функции (grab/poll/XTEST/spare/
  positioning/isTerminal) на Wayland НЕ вызываются, и наоборот — не смешивать пути.

### X11-бэкенд (`x11.go`)
- **Ввод — через xgb, не GTK.** У всплывшего `GTK_WINDOW_POPUP` GNOME отбирает
  фокус (focus-stealing prevention), поэтому клавиатуру грабим через
  `xproto.GrabKeyboard` на root и читаем клавиши поллингом в `glib.TimeoutAdd`.
  GTK нужен только для рисования (тема Yaru).
- **Захват клавиатуры — с ретраями:** сразу после Super mutter ещё держит
  клавиатуру (`AlreadyGrabbed`), отпускает после отпускания клавиш.
- **Вставка — нативный XTEST через запасной keycode** (подробный комментарий над
  `setupSpareKey`). Реальный keycode 'v' в русской раскладке даёт «м». Держим
  запасной неиспользуемый keycode, замапленный на 'v' во всех группах, и шлём его.
  Мапим при ОТКРЫТИИ попапа, возвращаем в NoSymbol через ~300мс после закрытия.
  Постоянно держать нельзя — mutter уведёт на него Super+V. Возврат сразу нельзя —
  Qt/Electron не успеют. НЕ спавнить `xdotool` (видимая задержка).
- **Терминалам — Ctrl+Shift+V** (детект по `WM_CLASS`), остальным — Ctrl+V.
- Все X-вызовы — из главного GTK-потока. Горутина сокета будит его только через
  `glib.IdleAdd`.

### Wayland-бэкенд (`wayland.go` + `internal/uinput`)
- **Попап — обычный `GTK_WINDOW_TOPLEVEL`.** Под Wayland он ПОЛУЧАЕТ фокус, поэтому
  клавиши читаем штатными GTK-сигналами (`key-press-event`); стрелки/PageUp/Home/End
  уходят в сфокусированный `ListBox` нативно — перехватываем только Enter/Escape.
  Никакого xgb-грабa. Не превращать обратно в override-redirect — фокуса не будет.
- **Скрытие — по `focus-out-event`** (клика-мимо через pointer-grab на Wayland нет).
- **Позиция — по центру** (`WIN_POS_CENTER_ALWAYS`). Задать окну координаты у курсора
  Wayland не даёт — не пытаться.
- **Вставка — `Shift+Insert` через `/dev/uinput`** (не XTEST — он до нативных
  Wayland-окон не доходит). `Insert` — функциональная клавиша, раскладко-независима.
  ВАЖНО: `Shift+Insert` в GUI-полях берёт CLIPBOARD, а в VTE-терминалах — PRIMARY,
  поэтому при вставке кладём выбранную запись в ОБА селекшна (`setClipboard` +
  `setPrimary` в `finishWayland`). Без PRIMARY в консоль вставлялась бы старая мышиная
  выделенка, а не выбранная запись. Детект окна под Wayland не нужен (и невозможен).
  Устройство создаём один раз при старте демона и переиспользуем (см. комментарий в
  `internal/uinput`). Env-override `CLIPMGR_PASTE=ctrlv`.
- **История — через XWayland-мост** (`startClipboardWatchWayland`). Фоновый wl-путь
  чужой буфер не видит (нет `data-control`), поэтому мониторим X11 CLIPBOARD, куда
  mutter зеркалит буфер: отдельное xgb-соединение к XWayland, XFIXES-уведомления о
  смене владельца, значение читаем сами (`ConvertSelection`→`SelectionNotify`→
  `GetProperty`, INCR пропускаем) — без внешних утилит, как CopyQ. Событийно (не
  поллинг) — быстрые копирования не теряются. Отдельное соединение живёт в своей
  горутине; `addToHistory` — через `glib.IdleAdd` (общий инвариант «X-вызовы
  shared-конекта — из GTK-потока» не нарушается: это ОТДЕЛЬНЫЙ конект). Требует только
  XWayland (`$DISPLAY`).
- **self-set при вставке не двигает историю:** `setClipboard` метит текст
  (`selfSetPending`/`selfSetText`), `ingestClipboard` его пропускает — выбранная запись
  остаётся на месте. Общий путь для X11 (owner-change) и Wayland (XFIXES).

### Общее
- **Буфером владеет сам GTK-демон** (`clip.SetText`), пока жив; внешние `xsel`/`xdotool`
  в рантайме не нужны ни на X11, ни на Wayland (чтение истории на Wayland — in-process
  по xgb).
- **gotk3 закреплён на v0.6.3.** v0.6.4 не собирается (недостающий импорт в gdk).
  Не обновлять бездумно.

## Коммиты (важно для changelog и релизов)

Используем **Conventional Commits**. От типа зависит, попадёт ли коммит в
changelog (его собирает git-cliff по `cliff.toml`).

Формат: `type(scope): краткое описание` (описание — на русском, тип — на английском).

**Попадают в changelog** (пользовательские изменения):
- `feat:` — новая функциональность → раздел «🚀 Features».
- `fix:` — исправление бага → «🐛 Bug Fixes».
- `perf:` — производительность → «⚡ Performance».
- любой тип со `scope` `security` (напр. `fix(security):`) или упоминанием
  security в теле → «🛡️ Security».
- **Ломающее изменение**: `!` после типа (`feat!:`) или `BREAKING CHANGE:` в теле
  → раздел «💥 Breaking Changes».

**НЕ попадают в changelog** (скрыты намеренно): `chore:`, `docs:`, `refactor:`,
`test:`, `ci:`, `build:`, `style:` и всё неконвенциональное.

**Зарезервировано:** `chore(release): vX.Y.Z` создаёт только workflow bump-version
— вручную так коммиты не оформлять.

Прочее: коммиты — по смысловым блокам; тело объясняет «почему», не только «что».
Комментарии в коде — на русском (как и весь проект).

## Релизы

Версия хранится в файле `VERSION` и зашивается в бинарник при сборке
(`-ldflags "-X main.version=…"`; локально — `dev`). Проверить: `clipmgr --version`.

Процесс (GitHub Actions):
1. Вручную запустить workflow **Bump Version** (`workflow_dispatch`, выбрать
   patch/minor/major) → он поднимает `VERSION`, коммитит `chore(release): vX.Y.Z`,
   ставит тег и пушит.
2. Пуш тега `vX.Y.Z` триггерит **Release**: сборка бинарника (`build.yml`),
   changelog за последний тег (git-cliff), публикация GitHub Release с артефактом
   `clipmgr-linux-x64`.

Требуется секрет репозитория **`REPOSITORY_PAT`** (PAT с `contents:write`) — иначе
пуш тега из bump-version не запустит release (ограничение GITHUB_TOKEN).
`ci.yml` гоняет сборку/`go vet`/golangci-lint на push в main и PR.
