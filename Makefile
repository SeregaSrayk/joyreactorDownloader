# joyreactorDownloader — Makefile с частыми командами.
#
# Wails CLI ищет wails.json в cwd, поэтому build/dev делаются из
# cmd/joyreactor-gui/. Тесты — из корня репозитория (модульный путь
# joyreactorDownloader/internal/... работает откуда угодно).
#
# Список команд: `make help` или просто `make`.

GUI_DIR  := cmd/joyreactor-gui
GUI_BIN  := $(GUI_DIR)/build/bin/joyreactorDownloader
EXE_NAME := joyreactorDownloader.exe

.DEFAULT_GOAL := help

.PHONY: help
help: ## Показать список команд
	@awk 'BEGIN { FS = ":.*?## "; printf "Targets:\n" } /^[a-zA-Z0-9_-]+:.*?## / { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ====== Сборка ======

.PHONY: build
build: kill ## Собрать production exe (cmd/joyreactor-gui/build/bin/joyreactorDownloader[.exe])
	cd $(GUI_DIR) && wails build -clean

.PHONY: build-nsis
build-nsis: kill ## Собрать exe + NSIS-инсталлер (Windows only)
	cd $(GUI_DIR) && wails build -clean -nsis

.PHONY: dev
dev: ## Запустить wails dev (hot-reload)
	cd $(GUI_DIR) && wails dev

.PHONY: run
run: build ## Собрать и запустить exe
	$(GUI_BIN)

.PHONY: cli
cli: ## Скомпилировать CLI (cmd/joyreactor-dl)
	go build -o bin/joyreactor-dl ./cmd/joyreactor-dl

# ====== Качество кода ======

.PHONY: test
test: ## Запустить юнит-тесты (без интеграции)
	go test ./...

.PHONY: test-integration
test-integration: ## Прогнать интеграционные тесты (требуют сеть → api.joyreactor.cc)
	go test -tags=integration ./...

.PHONY: vet
vet: ## go vet ./...
	go vet ./...

.PHONY: fmt
fmt: ## go fmt ./...
	go fmt ./...

.PHONY: check
check: fmt vet test ## fmt + vet + юнит-тесты

# ====== Обслуживание ======

.PHONY: kill
kill: ## Прибить запущенный exe (нужен перед wails build на Windows)
	-@taskkill //F //IM $(EXE_NAME) 2>/dev/null || true

.PHONY: clean
clean: ## Удалить build/ артефакты Wails и временные скрины
	rm -rf $(GUI_DIR)/build/bin $(GUI_DIR)/frontend/dist
	rm -f screenshots/*.png

.PHONY: deps
deps: ## Подтянуть Go-зависимости + frontend npm
	go mod tidy
	cd $(GUI_DIR)/frontend && npm install

# ====== UI-автоматизация (Windows/PowerShell) ======

.PHONY: screenshot
screenshot: ## Снимок окна приложения → screenshots/screen.png
	powershell -File scripts/screenshot.ps1

.PHONY: screenshot-list
screenshot-list: ## Перечислить окна с заголовком "Joyreactor Downloader"
	powershell -File scripts/screenshot.ps1 -List
