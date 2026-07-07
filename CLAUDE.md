# CLAUDE.md

Указания для работы над этим репозиторием. Подробности устройства — в
[ARCHITECTURE.md](./ARCHITECTURE.md).

## Что это

Нативная история буфера обмена для **GNOME (X11 + базовый Wayland)** — аналог
`Win+V`. Резидентный GTK-демон на Go: по горячей клавише (`Super+Ctrl+V`)
показывает попап-список записей (тема Yaru), стрелки/PageUp/PageDown/Home/End
двигают выбор, `Enter` вставляет выбранное в активное окно, `Escape` закрывает.

Один бинарник (`cmd/gnome-clipboard-history-native`), два бэкенда, выбор в рантайме (`isWayland()` в
`cmd/gnome-clipboard-history-native/wayland.go`):
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
  `/dev/uinput` (нужен доступ на запись). Доступ настраивается **автоматически**:
  `--install` на Wayland ставит udev-правило (эскалация pkexec/sudo), либо отдельно
  `gnome-clipboard-history-native --setup-input`; `.deb`/`.rpm` кладут то же правило сами (postinst под root).
  Правило комбинированное — `uaccess` (мгновенный ACL активному юзеру, без релогина) +
  `GROUP=input` как fallback. Единый источник текста правила — `uinput_setup.go`. См.
  `cmd/gnome-clipboard-history-native/uinput_setup.go`. Переключение раскладки настраивать через **GNOME
  Tweaks** (не Settings), иначе модификаторы «съедаются» и хоткей/вставка ломаются на
  2-й раскладке.
- **GNOME (mutter)**: горячая клавиша вешается через сам GNOME (gsettings custom
  keybinding), т.к. mutter держит `Super` и приложение не может перехватить его
  через XGrabKey. Работает и на X11, и на Wayland.
- Сборка: **Go 1.23+**, **cgo**, `libgtk-3-dev`. Рантайм: GTK3; на X11 — X-сервер с
  XTEST, на Wayland — доступ к `/dev/uinput` (см. выше про авто-настройку).

## Сборка и dev-цикл

```sh
go build -o gnome-clipboard-history-native ./cmd/gnome-clipboard-history-native

# перезапустить демона после пересборки (типичный цикл разработки):
# pkill -f, не -x: имя длиннее 15 симв. — ядро режет comm, матчим по cmdline.
pkill -f gnome-clipboard-history-native; sleep 0.3; rm -f "$XDG_RUNTIME_DIR/gnome-clipboard-history-native.sock"
setsid ./gnome-clipboard-history-native >>/tmp/gnome-clipboard-history-native.log 2>&1 &   # лог демона тут
```

- Клиент/«звонок» (то, что делает GNOME-хоткей): `./gnome-clipboard-history-native --show`.
- **Строго один инстанс** — демон проверяет сокет `$XDG_RUNTIME_DIR/gnome-clipboard-history-native.sock`
  перед стартом. Перед запуском нового всегда `pkill` старый.

## Как проверять (автотестов нет)

Само-тест без физической клавиатуры — синтетические клавиши доходят, т.к. ввод
идёт через xgb-grab на root:

```sh
./gnome-clipboard-history-native --show; sleep 0.4
xdotool key --clearmodifiers Down; xdotool key --clearmodifiers Return
xsel -ob        # проверить, что в буфер попал текст выбранной записи
```

Что важно проверять физически (синтетикой не покрыть надёжно): реальную вставку
в **консоль** (kitty → Ctrl+Shift+V) и в **обе раскладки** (баг с раскладкой уже
чинён remap'ом keycode).

## Структура

Стандартная Go-раскладка: бинарник в `cmd/`, приватные пакеты в `internal/`.
Модуль — `github.com/tihonove/gnome-clipboard-history-native`.

- `cmd/gnome-clipboard-history-native/` — package main, разрезан по файлам:
  - `main.go` — точка входа, диспатч флагов, `version`;
  - `client.go` — «звонок» `--show` (его дёргает GNOME-хоткей);
  - `install.go` — `--install`/`--uninstall`, gsettings-helpers;
  - `daemon.go` — резидентная часть: сокет, инициализация бэкенда, слушалка
    буфера, ловля текста/картинок, `setClipboard`/`setClipboardImage`;
  - `item.go` — модель записи истории `clipItem` (текст ИЛИ картинка), дедуп по
    ключу, байтовый бюджет картинок, декод PNG→pixbuf;
  - `popup.go` — общее для бэкендов: CSS, конструктор содержимого `buildPopupBox()`
    (текст — Label, картинка — cover-рендер в `DrawingArea` через cairo);
  - `x11.go` — **X11-бэкенд**: попап у курсора, grab/poll, XTEST-вставка через
    запасной keycode, позиционирование;
  - `wayland.go` — **Wayland-бэкенд**: `isWayland()`, `showPopupWayland()`,
    `finishWayland()`, XWayland-мост истории.
  - `uinput_setup.go` — единоразовая настройка доступа к `/dev/uinput`
    (`--setup-input`/`--remove-input`): udev-правило + эскалация pkexec/sudo через
    ре-экзек своего же бинарника (скрытые `__setup-input-root`/`__remove-input-root`).
- `internal/uinput/` — виртуальная клавиатура через `/dev/uinput` (вставка на
  Wayland): `Init()`, `Close()`, `InjectPaste()`, `InjectPasteCtrlV()` (для картинок),
  `HasAccess()`, `DevPath`.
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
- **Позиция — по центру** (`WIN_POS_CENTER_ALWAYS`). Под-курсорное / в-активном-окне
  позиционирование (как X11 `popupXY`) на нативном toplevel НЕВОЗМОЖНО: mutter
  игнорирует `gtk_window_move`, а курсор не достать надёжно (`QueryPointer` по XWayland
  свеж лишь над XWayland-окном; `_NET_ACTIVE_WINDOW` для нативных wl-окон = `None`).
  Единственный позиционируемый попап — override-redirect через XWayland
  (`GDK_BACKEND=x11`), но `XGrabKeyboard` на root под mutter вернёт `Success` и клавиш
  не отдаст (фокус уходит wl-окну) — потому бэкенд и на нативном toplevel. Лучшее
  достижимое — центр на мониторе, который выберет mutter (обычно активный). Не пытаться
  ставить у курсора — тот же класс ограничения, что `data-control`/XTEST.
- **Вставка — `Shift+Insert` через `/dev/uinput`** (не XTEST — он до нативных
  Wayland-окон не доходит). `Insert` — функциональная клавиша, раскладко-независима.
  ВАЖНО: `Shift+Insert` в GUI-полях берёт CLIPBOARD, а в VTE-терминалах — PRIMARY,
  поэтому при вставке кладём выбранную запись в ОБА селекшна (`setClipboard` +
  `setPrimary` в `finishWayland`). Без PRIMARY в консоль вставлялась бы старая мышиная
  выделенка, а не выбранная запись. Детект окна под Wayland не нужен (и невозможен).
  Устройство создаём один раз при старте демона и переиспользуем (см. комментарий в
  `internal/uinput`). Env-override `GCHN_PASTE=ctrlv`.
- **Доступ к `/dev/uinput` — единоразовая привилегированная настройка** (`uinput_setup.go`),
  НЕ каждая вставка. Проверяй `uinput.HasAccess()` перед эскалацией — если доступ уже
  есть (напр. `.deb` положил правило, или узел world-writable), sudo/pkexec НЕ дёргать.
  `--install` на Wayland настраивает сам, но только если `!HasAccess() && !ruleInstalled()`.
  Привилегированную запись делает ре-экзек своего же бинарника (скрытый сабкоманд), а не
  shell-heredoc. Wayland по дизайну запрещает инжект в чужие окна — это цена, как у CopyQ
  (тот же `/dev/uinput` через ydotool); zero-setup-пути без udev-правила нет.
- **История — через XWayland-мост** (`startClipboardWatchWayland`). Фоновый wl-путь
  чужой буфер не видит (нет `data-control`), поэтому мониторим X11 CLIPBOARD, куда
  mutter зеркалит буфер: отдельное xgb-соединение к XWayland, XFIXES-уведомления о
  смене владельца. По уведомлению спрашиваем `TARGETS` и берём **текст** (`UTF8_STRING`)
  либо **картинку** (`image/png`), читая значение сами (`ConvertSelection`→
  `SelectionNotify`→`GetProperty`) — без внешних утилит, как CopyQ. **Крупные значения
  (скриншоты) приходят по INCR** — читаем кусками (`readSelectionBytes`): свойство типа
  `INCR` — маркер, его удаление сигналит владельцу слать куски, дальше на каждый —
  `PropertyNotify(NewValue)`, пустой кусок = конец (нужен `EventMaskPropertyChange` на
  окне-реквесторе). Событийно (не поллинг) — быстрые копирования не теряются. Отдельное
  соединение живёт в своей горутине; `ingest*` — через `glib.IdleAdd` (общий инвариант
  «X-вызовы shared-конекта — из GTK-потока» не нарушается: это ОТДЕЛЬНЫЙ конект).
  Требует только XWayland (`$DISPLAY`).
- **self-set при вставке не двигает историю:** `setClipboard`/`setClipboardImage` метят
  запись хеш-ключом (`selfSetPending`/`selfSetKey`; текст vs картинка — общий механизм),
  `ingestText`/`ingestImage` его пропускают — выбранная запись остаётся на месте. Общий
  путь для X11 (owner-change) и Wayland (XFIXES).

### Общее
- **Буфером владеет сам GTK-демон** (`clip.SetText`), пока жив; внешние `xsel`/`xdotool`
  в рантайме не нужны ни на X11, ни на Wayland (чтение истории на Wayland — in-process
  по xgb).
- **gotk3 закреплён на v0.6.3.** v0.6.4 не собирается (недостающий импорт в gdk).
  Не обновлять бездумно.

## Коммиты (важно для changelog и релизов)

Используем **Conventional Commits**. От типа зависит, попадёт ли коммит в
changelog (его собирает git-cliff по `cliff.toml`).

Формат: `type(scope): short description`. **Сообщения коммитов пишем на английском**
(заголовок и тело) — changelog собирается из них, поэтому релизы тоже выходят на
английском.

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
**Сообщения коммитов — на английском.** Комментарии в коде и документация
(CLAUDE.md, ARCHITECTURE.md) остаются на русском (как и весь проект).

## Релизы

Версия хранится в файле `VERSION` и зашивается в бинарник при сборке
(`-ldflags "-X main.version=…"`; локально — `dev`). Проверить: `gnome-clipboard-history-native --version`.

Процесс (GitHub Actions):
1. Вручную запустить workflow **Bump Version** (`workflow_dispatch`, выбрать
   patch/minor/major) → он поднимает `VERSION`, коммитит `chore(release): vX.Y.Z`,
   ставит тег и пушит.
2. Пуш тега `vX.Y.Z` триггерит **Release** (`build → package → release → apt-repo`):
   бинарник (`build.yml`), `.deb` (nfpm), changelog (git-cliff), GitHub Release
   (бинарник + `.deb`) и публикация в apt-репозиторий — см. «Дистрибуция» ниже.

Требуется секрет репозитория **`REPOSITORY_PAT`** (PAT с `contents:write`) — иначе
пуш тега из bump-version не запустит release (ограничение GITHUB_TOKEN).
`ci.yml` гоняет сборку/`go vet`/golangci-lint на push в main и PR. **golangci-lint
включает `gofmt`** — перед пушем прогоняй `gofmt -l .` (пусто = ок), иначе CI красный.

## Дистрибуция (apt-репозиторий)

Ставится через **свой подписанный apt-репозиторий на GitHub Pages** (ветка
`gh-pages`), не PPA. Всё автоматизировано в `release.yml`.

**Пакет (`nfpm`, `packaging/nfpm.yaml`):** кладёт бинарник в `/usr/bin/gnome-clipboard-history-native` и
**статическое udev-правило** (`uaccess`) в `/usr/lib/udev/rules.d/` — тот же
`pkgUdevRulePath`, что проверяет `--setup-input`, поэтому `gnome-clipboard-history-native --install` видит
готовый доступ и не эскалируется. postinst только активирует udev
(`modprobe`/`udevadm`); per-user часть (хоткей, автозапуск, старт демона) делает
**`gnome-clipboard-history-native --install`** уже в сессии — из root-postinst это невозможно (демона
postinst НЕ запускает и `--install` НЕ зовёт: был баг с `/root`).

**Релизный пайплайн** (`release.yml` по тегу): `build` (бинарник) → `package`
(`.deb`) → `release` (GitHub Release: бинарник + `.deb` + changelog) и `apt-repo`
(`reprepro` подписывает, пушит состояние в `gh-pages` и **деплоит сайт через
Actions**). reprepro держит одну версию (pool не пухнет), повторный тот же тег не
роняет job (пустой коммит терпится, деплой всё равно идёт).

**Деплой Pages — через Actions (`build_type=workflow`), НЕ сервинг ветки.** Ветка
`gh-pages` — только персистентный стейт-стор reprepro (pool/db/dists). Обслуживаемое
дерево (dists/ + pool/ + статика, без conf/db/.git) джоба `apt-repo` грузит
артефактом (`upload-pages-artifact`) и деплоит синхронно (`deploy-pages`) в ТОМ ЖЕ
прогоне — статус виден сразу, отдельного флакающего воркфлоу «pages build and
deployment» больше нет. Ручной передеплой текущего состояния без релиза —
`pages.yml` (`workflow_dispatch`); он же нужен для разового cutover при переключении
legacy→workflow, чтобы сайт не лёг.

**Установка (юзеру):** оба скрипта тонкие (логика в бинарнике), **запускать без
sudo** (сами эскалируются где надо), в конце зовут `gnome-clipboard-history-native --install`:
- apt + автообновления: `curl -fsSL <pages>/install.sh | sh`;
- без apt: `curl -fsSL <pages>/install-standalone.sh | sh` (качает бинарник).

**Разовая настройка репо (вне кода, руками в Settings):** секрет
`APT_GPG_PRIVATE_KEY` (приватный ключ подписи; публичный экспортит CI); Pages →
**Source: GitHub Actions** (`build_type=workflow`, не «Deploy from a branch»);
Actions → Read and write permissions (иначе push в `gh-pages` падает). **Окружение
`github-pages` должно разрешать деплой с тегов**: релиз идёт с тега `vX.Y.Z`, а
дефолтная политика окружения пускает только ветки — без тег-политики `apt-repo`
падает на «Tag … is not allowed to deploy to github-pages». Добавить паттерн:
`gh api -X POST repos/OWNER/REPO/environments/github-pages/deployment-branch-policies
-f name='v*.*.*' -f type=tag` (или в Settings → Environments → github-pages →
Deployment branch and tags → добавить тег-правило `v*.*.*`).

**Апдейт-грабля:** `apt upgrade` меняет файл на диске, но запущенный демон остаётся
старым в памяти до рестарта/логина (postinst из root чужую сессию не рестартит).
Наивный self-restart потерял бы in-memory историю — открытый вопрос, **issue #2**.

### Дев-инстанс (тестить, не толкаясь с установленным)

Демон — single-instance на сокете; параллельный дев поднимается через env
(`sockPath`/`install.go`):
- `GCHN_SOCK` — свой сокет;
- `GCHN_NAME` — свой слот gsettings и **без автозапуска** (`isDevInstance`);
- `GCHN_HOTKEY` — своя клавиша; сокет прокидывается в команду хоткея.

Раз: `GCHN_SOCK=… GCHN_HOTKEY='<Super><Control>b' GCHN_NAME=gnome-clipboard-history-native-dev
./gnome-clipboard-history-native-dev --install`. Дальше каждый сеанс просто: `GCHN_SOCK=… ./gnome-clipboard-history-native-dev`.
