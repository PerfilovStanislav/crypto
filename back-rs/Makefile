# Сборка и запуск back-rs. `make` без аргументов — список команд.
#
# Бинарник собирается один раз и потом пересобирается только при изменении кода. Набор фич
# всегда один (gpu): если собирать то с ним, то без, cargo будет пересобирать заново.
# PROFILE=fast (по умолчанию) пересобирается за секунды; PROFILE=release — fat LTO, ~50 с
# на пересборку, перебор на CPU на ~5% быстрее.
# Конфиги берутся из ../configs (или из CONFIG_DIR).

PROFILE  ?= fast
FEATURES ?= gpu
CARGO_FLAGS := --profile $(PROFILE) --features $(FEATURES)
BIN := target/$(PROFILE)/crypto-back

.PHONY: help build run gpu cpu server test test-clickhouse bench golden lint clean

help: ## список команд
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

build: ## собрать (cargo пересобирает только изменённое)
	cargo build $(CARGO_FLAGS)

run: build ## запустить по configs/config.yaml
	$(BIN)

gpu: build ## перебор стратегий на GPU
	ANALYZER_ACTIVE=true ANALYZER_GPU=true $(BIN)

cpu: build ## перебор стратегий на CPU
	ANALYZER_ACTIVE=true ANALYZER_GPU=false $(BIN)

server: build ## только синк свечей и HTTP API для фронта, без перебора
	ANALYZER_ACTIVE=false HTTP_ACTIVE=true $(BIN)

test: ## unit-тесты и сверка с Go, включая GPU
	cargo test $(CARGO_FLAGS)

test-clickhouse: ## синк в живой ClickHouse во временную таблицу и сверка
	cargo test $(CARGO_FLAGS) --test clickhouse -- --ignored --nocapture

bench: ## бенчмарк перебора (criterion, release-оптимизация)
	cargo bench --bench analyzer

golden: ## перегенерировать фикстуры сверки с Go
	mkdir -p tests/fixtures
	cd ../back/src && GOLDEN_OUT=$(CURDIR)/tests/fixtures/golden.json go test ./analyzer -run TestGolden -count=1

lint: ## rustfmt и clippy
	cargo fmt --check
	cargo clippy $(CARGO_FLAGS) --all-targets

clean: ## удалить target/
	cargo clean
