<!-- handoff:task:cd7d0285-f542-4671-8f7d-1632ce84c984 -->
# Инициализация репозитория swarm-hpa

- [ ] **Ветка:** master
- [ ] **Дата:** 2026-07-03
- [ ] **Режим:** fast
- [ ] **Исходный репозиторий:** https://github.com/Aleksey512/swarm-hpa.git

## Описание

Привести локальный рабочий каталог `/home/www/test` в соответствие с исходным проектом
`Aleksey512/swarm-hpa` — Go-демоном горизонтального автомасштабирования и самолечения
задач для Docker Swarm. Сейчас каталог пуст (на `master` нет коммитов, исходников нет),
поэтому инициализация сводится к заливке кода из апстрима и базовой настройке AI-контекста.

## Settings

- [ ] **Testing:** no (тесты пропускаются — пользователь явно отключил)
- [ ] **Logging:** standard
- [ ] **Docs:** no (пользователь явно отключил; `/aif-implement` эмитит `WARN [docs]` и продолжает)

## Tasks

### Phase 1 — Import upstream source

#### Task 1: Залить исходники swarm-hpa из апстрима

Загрузить содержимое `https://github.com/Aleksey512/swarm-hpa.git` (ветка `main`,
HEAD `47cf56ff235813d84aba9db00f5e8764f132eeb7`) в текущий рабочий каталог, не затирая
существующие AI-Factory артефакты (`.ai-factory/`, `.claude/`, `.codex/`, `.opencode/`,
`.ai-factory.json`).

Подход:
1. Добавить апстрим как remote: `git remote add upstream https://github.com/Aleksey512/swarm-hpa.git`
2. Выполнить `git fetch upstream main`
3. Слить в `master` без слияния AI-Factory конфигурации:
   `git merge upstream/main --allow-unrelated-histories --no-edit`
   - [ ] При конфликтах в `.ai-factory/` или служебных каталогах — разрешить в пользу
     текущей (`ours`) версии; для прочих файлов принимать апстрим.
4. Убедиться, что после слияния присутствуют: `cmd/`, `internal/`, `deploy/`, `docs/`,
   `examples/`, `Makefile`, `Dockerfile`, `go.mod`, `go.sum`, `README.md`, `LICENSE`,
   `AGENTS.md`, `.golangci.yml`, `.github/`, `.gitignore`, `.dockerignore`.
5. Удалить временный remote: `git remote remove upstream`.

**Файлы:** весь корень репозитория.
**Логирование:** стандартные INFO-сообщения о шагах слияния и результатах.

#### Task 2: Верифицировать сборку

Убедиться, что импортированный проект собираается локально.

1. Проверить наличие `go` (или установить, если отсутствует — сообщить пользователю).
2. Выполнить `go mod download` для загрузки зависимостей.
3. Выполнить `make build` (цель определена в апстримном `Makefile`,产物: `bin/swarm-hpa`).
4. Проверить, что `./bin/swarm-hpa --version` отдаёт версию (или `--help` показывает CLI).
5. Если `make build` недоступен (например, нет `make`) — fallback: `go build -o bin/swarm-hpa ./cmd/swarm-hpa`.

**Файлы:** `bin/swarm-hpa` (артефакт сборки).
**Логирование:** вывод сборщика как есть + INFO-резюме результата.

### Phase 2 — AI-Factory контекст

#### Task 3: Сгенерировать DESCRIPTION.md

Создать `.ai-factory/DESCRIPTION.md` на основе README.md и реальной структуры кода.
Описание должно зафиксировать:

- [ ] **Стек:** Go (версия из `go.mod`), CLI-демон, без БД/ORM.
- [ ] **Назначение:** горизонтальное автомасштабирование Docker Swarm + самолечение
  `pending`-задач + ребалансировка нагрузки.
- [ ] **Ключевые модули (из `internal/`):** краткое перечисление после просмотра дерева
  каталога (manager reconcile loop, metrics providers: stats/prometheus/agents,
  healer, rebalancer, guarded act path).
- [ ] **Сборка/запуск:** `make build`, `./bin/swarm-hpa`, dry-run по умолчанию.
- [ ] **Конвенции:** правила из `.golangci.yml`, AGENTS.md (если присутствует).

**Файлы:** `.ai-factory/DESCRIPTION.md`.
**Логирование:** INFO при генерации.

#### Task 4: Сгенерировать ARCHITECTURE.md

Создать `.ai-factory/ARCHITECTURE.md`, фиксирующий выбранную архитектуру и слойовые
границы, которые почерпнуты из README и `internal/`:

- [ ] **Паттерн:** Manager/Agent split + единый reconcile-loop (observe → decide → act).
- [ ] **Слои:**
  - [ ] `cmd/` — точка входа, парсинг флагов, выбор роли (manager/agent).
  - [ ] `internal/observe` (или эквивалент) — чтение Swarm-состояния (services, tasks, nodes, labels).
  - [ ] `internal/decide` (чистое ядро) — решения scale/heal/rebalance без побочных эффектов.
  - [ ] `internal/act` — единый guarded-path (dry-run + opt-in labels + per-service cooldown),
    вызывает `ServiceUpdate`/`ForceUpdate` через Docker API клиент.
  - [ ] Metrics providers: stats / prometheus / agents (DI-переключаемая абстракция).
  - [ ] HTTP-эндпоинт агента (`POST /v1/report`) и собственный `/metrics` менеджера.
- [ ] **Правила зависимостей:** `cmd → internal/{observe,decide,act}`; `decide` не зависит от
  Docker SDK; `act` — единственное место мутаций состояния Swarm.
- [ ] **Имена пакетов и путей уточнить по фактическому дереву `internal/` после импорта.**

**Файлы:** `.ai-factory/ARCHITECTURE.md`.
**Логирование:** INFO при генерации.

## Commit Plan

4 задачи — меньше 5, поэтому единый финальный коммит после всех задач (без промежуточных чекпойнтов).

Предлагаемое сообщение:

```
chore(init): import swarm-hpa upstream and set up AI-Factory context
```

## Замечания

- [ ] Коммит создаётся только по явной команде пользователя (`/aif-commit`); план лишь фиксирует предложение.
- [ ] Если `git merge --allow-unrelated-histories` невозможен из-за отсутствия HEAD-коммита на `master`,
  альтернатива: `git pull upstream main --allow-unrelated-histories` или `git read-tree` + ручная
  индексация. Решение принимается на этапе Task 1.
- [ ] Запуск/тесты самого демона требуют Docker Swarm кластера — в окружении CI/локали это может быть
  недоступно; достаточно `--version`/сборки.
