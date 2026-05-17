# CLAUDE.md

## Назначение проекта

Кросс-платформенное (Windows / macOS / Linux) десктопное приложение для
**скачивания** картинок с Joyreactor по фильтрам, аналогичным штатному поиску
сайта. Главный артефакт — один статичный бинарник без рантаймов:
`cmd/joyreactor-gui/build/bin/joyreactorDownloader[.exe|.app]`.

Стек: Go + [Wails v2](https://wails.io) (Go-бэк + системный WebView +
HTML/CSS/JS-фронт). Wails использует нативный для платформы движок: WebView2
(Edge Chromium) на Windows, WKWebView на macOS, WebKitGTK на Linux. Параллельно
сохранён CLI (`cmd/joyreactor-dl/`) — удобен для отладки логики.

Платформенные особенности (что разное по OS):
- Системные toast-уведомления — Windows (нативные WinRT), на macOS/Linux no-op.
- «Открыть папку» — `explorer`/`open`/`xdg-open` соответственно.
- Single-instance lock — Wails встроенный механизм работает на всех трёх.

Установка Wails-зависимостей на Linux: `libwebkit2gtk-4.0-dev libgtk-3-dev pkg-config`
(см. `.github/workflows/build.yml` для актуального списка).

## Источник данных

Метаданные постов/медиа — официальный GraphQL API:

- Endpoint: `https://api.joyreactor.cc/graphql` (POST JSON `{query, variables}`).
- Playground: https://api.joyreactor.cc/graphql-playground
- Relay Object Identification: глобальный `id` = `base64("<Type>:<numericId>")`.
- Полный дамп схемы — `api/schema/introspection.json`.
- Лимиты жёсткие — `internal/graphql.Client` сериализует запросы и держит `minDelay`,
  на HTTP 429 возвращает `ErrRateLimited`.

### Ключевые типы

- Точки входа: `tag(name).postPager(type)`, `user(username).postPager`,
  **`search(...)`** (главный фильтр для downloader'а), `weekTopPosts/yearTopPosts`,
  `changedPosts(day)`, `node(id)`.
- Enum `PostLineType = ALL|NEW|GOOD|BEST|...` — для feed-режима `Tag.postPager`.
- Enum `AttributeType = PICTURE|YOUTUBE|VIMEO|...` — качаем только `PICTURE`.
- Enum `ImageType = PNG|JPEG|GIF|BMP|TIFF|MP4|WEBM|WEBP` — расширение + тип медиа.
- **У `Image` нет URL-поля.** Файл строится по `PostAttributePicture.id`,
  а **не** по `Image.id` (на старых постах оба совпадают, на новых — расходятся).
  Канонично: `https://img10.joyreactor.cc/pics/post/full/-<attrNumericId>.<ext>`.
  В коде это делает метод `graphql.Attribute.FileURL()`.

### Авторизация

Cookie-based. Мутация `login(name, password): Query!` ставит сессионный cookie,
который `internal/graphql.Client.jar` (`net/http/cookiejar`) сам прицепляет к
последующим запросам. Сессия сохраняется в `%APPDATA%/joyreactorDownloader/session.json`
(0600 perms), при старте GUI восстанавливается через `Client.LoadSession`.

Анонимный доступ покрывает большинство публичных запросов (включая `showOnlyNsfw`),
но для `searchInMyFavorites`, `friendPostPager`, `favoritePostPager` и (по словам
пользователя) части контента нужен логин.

## Архитектура

Раскладка следует [golang-standards/project-layout](https://github.com/golang-standards/project-layout):

```
cmd/
├── joyreactor-gui/          Wails GUI-приложение
│   ├── main.go              Wails entry: окно, embed assets, single-instance lock
│   ├── gui.go               Go-структура GUI, методы которой видны JS-фронту
│   ├── jobs.go              jobManager — несколько задач параллельно
│   ├── proxy.go             прокси CDN-картинок (Referer для anti-hotlink)
│   ├── window.go            persistent размер окна (%APPDATA%/window.json)
│   ├── presets.go           per-preset filter snapshot + outdir
│   ├── authors.go           локальный кэш авторов для autocomplete
│   ├── pause_gate.go        channel-based pause primitive
│   ├── wails.json           конфиг Wails (CLI ищет относительно него)
│   ├── frontend/            UI (HTML/CSS/vanilla JS, сборка Vite)
│   └── build/               иконки, Windows manifest, NSIS-инсталлер шаблон
└── joyreactor-dl/main.go    CLI-точка входа (параллельный артефакт для отладки)

internal/                    приватное ядро, переиспользуется GUI и CLI
├── graphql/                 Search/TagPosts/UserPosts/Login/Me, Relay ID, FileURL, cookie jar
├── filter/                  Criteria + клиент-сайд Match* методы
├── client/                  HTTP-клиент для скачивания бинарников с CDN
├── downloader/              сохранение файлов на диск (.part → rename) + манифест дедуп
└── app/                     не-Wails оркестратор (используется CLI); GUI имеет
                             свой конвейер в cmd/joyreactor-gui/gui.go.produce(...)

api/schema/                  дамп GraphQL-схемы для офлайн-сверки
docs/                        документация + screenshots для README
scripts/                     PowerShell-автоматизация UI (скриншоты + ввод)
screenshots/                 gitignored — место для скриншотов автоматизации
.github/workflows/           CI (build матрица на 3 OS + auto Release на v* тегах)
```

Слой ядра (`internal/*`) одинаков для GUI и CLI — единственный источник правды
по запросам/фильтрам/скачиванию.

## Что покрыто `filter.Criteria`

- **GraphQL `Query.search`** напрямую: `Query`, `Tags`, `Username`, `MinRating`/`MaxRating`
  (`*int`, nil = безграничный), `Sort`, `ShowNsfw`/`OnlyNsfw`/`ShowUnsafe`, `OnlyFavorite`.
- **Клиент-сайд** (после получения постов): `ExcludeTags`, `MediaKind`, `MinWidth`/`MinHeight`,
  `DateFrom`/`DateTo`.
- **Контроль выполнения**: `Limit`.

Методы фильтра: `MatchImage`, `MatchPostDate`, `MatchPostTags`.

## Команды разработки

Все частые команды есть в `Makefile` (быстрые шорткаты). Windows без `make` —
`powershell -File scripts/make.ps1 <target>` делает то же самое.

```sh
# Шорткаты (Makefile / make.ps1):
make build                             # → cmd/joyreactor-gui/build/bin/joyreactorDownloader[.exe]
make dev                               # wails dev (hot-reload)
make run                               # build + запуск exe
make test                              # юнит-тесты
make check                             # fmt + vet + test
make kill                              # прибить запущенный exe (нужен перед wails build)
make clean                             # снести build/bin, frontend/dist, тестовые скрины
make screenshot                        # скрин окна → screenshots/screen.png
make help                              # полный список

# Без Makefile (то же самое руками). Wails CLI ищет wails.json в cwd:
cd cmd/joyreactor-gui
wails dev                              # hot-reload dev
wails build -clean                     # production
cd ../..

# CLI (отладка/скриптинг)
go run ./cmd/joyreactor-dl -h
go run ./cmd/joyreactor-dl -tag art -min-rating 200 -limit 5 -out ./downloads

# Тесты — из корня (модульные пути joyreactorDownloader/internal/... работают везде)
go test ./...                          # юнит
go test -tags=integration ./...        # + удар по живому API

# UI-автоматизация (скриншоты + ввод). Все скрипты в scripts/, ASCII-only.
powershell -File scripts/screenshot.ps1                       # → screenshots/screen.png (gitignored)
powershell -File scripts/screenshot.ps1 -List                 # перечислить окна с указанным заголовком
powershell -File scripts/screenshot.ps1 -Full                 # снимок основного монитора целиком
powershell -File scripts/click.ps1 -X 240 -Y 58               # ЛКМ клик (по window-relative координатам)
powershell -File scripts/click.ps1 -X 240 -Y 58 -Right         # ПКМ
powershell -File scripts/key.ps1 -Key Escape                  # клавиша (Escape/Enter/Tab/F5/Up/Down/…)
powershell -File scripts/type.ps1 -Text "art"                 # ввод текста (Cyrillic OK, авто-escape SendKeys-метасимволов)
powershell -File scripts/scroll.ps1 -X 590 -Y 400 -Delta -480 # wheel (отрицательный delta = вниз)
powershell -File scripts/drag.ps1 -X1 100 -Y1 300 -X2 500 -Y2 600  # drag с интерполяцией пути
```

`scripts/screenshot.ps1` использует `PrintWindow` c флагом `PW_RENDERFULLCONTENT` —
снимает содержимое окна WebView2 напрямую через DC, минуя z-order (можно даже
если окно перекрыто другим). Заголовок матчится строго (`-eq`), чтобы не
зацеплять браузерные вкладки. Остальные скрипты переиспользуют общую библиотеку
`scripts/_lib.ps1` (P/Invoke + поиск окна) — все они работают в
window-relative координатах, чтобы перенос окна не ломал автоматизацию.

Все .ps1 ASCII-only — Windows PowerShell 5.1 без BOM читает .ps1 в системной
кодировке (cp1251 на русской Windows), не-ASCII символы ломают парсер.

Кропы для точной локации UI-элемента (когда thumbnail в Read размывает
мелкий шрифт) можно делать inline через `System.Drawing.Bitmap.DrawImage`,
сохраняя в ту же `screenshots/` папку.

## Конвенции

- Модуль `joyreactorDownloader`, Go 1.22+. Зависимости только нужные:
  Wails (для GUI), стандартная библиотека (для всего остального).
- Прикладной код — в `internal/`, чтобы запретить импорт извне.
- Все долгие операции принимают `context.Context` первым аргументом.
- HTTP/GraphQL клиенты в одном экземпляре на запуск.
- Уважать рейт-лимиты Joyreactor: задержки между запросами, разумная конкурентность.
- **После любых правок Go/JS/CSS сам пересобирай exe** командой `wails build -clean`
  (из `cmd/joyreactor-gui/`), не оставляй это пользователю. Пользователь тестирует именно
  `cmd/joyreactor-gui/build/bin/joyreactorDownloader.exe`, а не `wails dev`, поэтому без
  пересборки изменения до него не дойдут.
- **Если exe запущен — закрывай его сам** перед `wails build -clean` (через
  `taskkill //F //IM joyreactorDownloader.exe`), не проси пользователя.
  Wails-сборка не может перезаписать запущенный exe, и пользователь не хочет
  каждый раз вручную закрывать окно.

## Что НЕ делать

- Не обходить капчу/антибот, не делать массовый скрейп — инструмент для личного
  использования с разумными лимитами.
- Не хранить пароль в открытом виде — только сессионный cookie в `session.json`.
- Не коммитить `cmd/joyreactor-gui/frontend/wailsjs/` (автогенерируется),
  `cmd/joyreactor-gui/frontend/{node_modules,dist}/`,
  `cmd/joyreactor-gui/build/bin/`, `.env`.
- Не плодить абстракции «на будущее»: интерфейсы только когда появляется вторая реализация.

## Текущий статус

**Готово (полный конвейер):**
- GraphQL → клиент-сайд фильтры → параллельная загрузка с дедупом.
- Wails-приложение, тёмная тема, форма фильтров 1:1 с UI Joyreactor + extras,
  иконка `joyreactor.cc` (apple-touch-icon, 192×192), `cmd/joyreactor-gui/build/bin/joyreactorDownloader.exe` ~11MB.
- **Auth**: cookie-jar + `Login/Logout/Me` + сохранение сессии в `%APPDATA%`.
- **Backoff**: `Client.Do` ретраит на `ErrRateLimited` экспоненциально (1s → 30s, 6 попыток).
- **Грид превьюх**: миниатюры из `Post.ThumbnailURL()` (~3KB), пагинация
  «Загрузить ещё», клик → открывает пост в браузере. Бейдж количества картинок
  в углу плитки если в посте > 1 изображения.
- **Кросс-ранный манифест**: `<outDir>/.manifest.json` с ключами `attr.ID`.
- **Прогресс-бар**: тонкая полоса с заливкой, indeterminate-анимация без лимита,
  серый «paused»-стейт.
- **Pause / Resume / Cancel**: `pauseGate` (channel-based) гейтит и воркеры, и
  продюсера. Кнопки «⏸ Пауза» / «▶ Продолжить» / «Отменить» — на каждой задаче в
  очереди. Resume также вызывается из Cancel для разблокировки заблокированных Wait'ов.
- **Очередь задач**: `jobManager` в `jobs.go` хранит и запускает несколько
  скачиваний одновременно. Каждая задача — независимая конфигурация (фильтры,
  папка, лимит, воркеры), свой `pauseGate`, свой `ctx`, свой `downloader`
  (значит, свой `.manifest.json`). Параллелизм безопасный: `gql.Do` сериализует
  API-запросы внутри Client'а, скачивание бинарников от CDN не конфликтует.
  Кнопка «＋ Добавить в очередь» снимает snapshot формы и спавнит задачу.
  События `job:update` (с полным `JobView`) и `job:removed` (с id) пушатся во
  фронт, который держит `state.jobs` и рендерит карточки задач между секцией
  фильтров и гридом превью. Имя задачи генерится автоматически по фильтрам
  (`defaultJobName`) либо задаётся пользователем.
- **OS-toast**: при завершении/отмене/ошибке скачивания зовётся `go-toast/v2.Push()`
  с заголовком, кратким summary и кнопкой «Открыть папку». На Windows
  выскакивает нативное системное уведомление.
- **Пресеты фильтров**: `%APPDATA%/joyreactorDownloader/presets.json`. Селект в
  шапке + «Сохранить как…» / «Удалить». Пресет хранит все поля формы (теги,
  рейтинг, NSFW-флаги, тип медиа, размеры, даты, лимит, воркеры), кроме папки.
- **Автокомплит тегов**: при вводе в поле тегов (и в «Исключить теги») дёргает
  `Query.tagAutocomplete(mask)` с дебаунсом 200мс, показывает дропдаун с именем,
  счётчиком постов и NSFW-бейджем. Управление: ↑/↓ / Enter / Esc / клик. Работает
  для англ. и кириллицы. Запросы версионируются, чтобы устаревшие ответы не
  перетирали свежие.
- **Заблокированные теги из профиля**: после логина GUI асинхронно тянет
  `me { blockedTags { name } }` и кэширует список. Под чекбоксом «Учитывать
  заблокированные теги» (по умолчанию включён, дизейблится без логина) этот
  список мержится в `ExcludeTags` перед каждым Search/Download — гарантирует,
  что блокировки применяются независимо от того, фильтрует ли их сам сервер.
  Эмиттится `auth:blocked-tags` с count при login/logout/startup.
- **Помощь по авторам** (у API нет `userAutocomplete`):
  1. **Локальный кэш** — на каждый успешный `Search` GUI пишет авторов
     возвращённых постов в `%APPDATA%/joyreactorDownloader/authors.json` со
     счётчиком встречаемости (топ-1000, прунинг до 800 по убыванию count).
     Тот же `attachAutocomplete` рисует выпадайку (case-insensitive containment).
  2. **Live-валидация** — после паузы 350мс под полем «Автор» дёргается
     `Query.user(username: $)`. «✓ kuiwi · N постов» / «не найден» / «…».
- **Открыть папку**: кнопка зовёт `explorer <outDir>`.
- CLI остаётся как параллельный артефакт.

**Тесты:**
- `go test ./...` — юнит-тесты по ID/URL/манифесту.
- `go test -tags=integration ./...` — реальный удар по `api.joyreactor.cc`.