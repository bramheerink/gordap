.PHONY: help build build-tools test test-race test-realworld test-conformance install-openrdap install-validator validate lint clean run demo demo-synth seed stress stress-aggregate

help:
	@echo "gordap targets:"
	@echo "  make build               — build ./cmd/gordap"
	@echo "  make test                — unit + e2e tests"
	@echo "  make test-race           — tests under the race detector"
	@echo "  make test-realworld      — boot gordap + RFC assertions + interop checks"
	@echo "  make test-conformance    — run ICANN RDAP Conformance Tool (Docker)"
	@echo "  make validate            — run rdap-org/validator against live demo (Docker)"
	@echo "  make install-openrdap    — install openrdap/rdap CLI for interop tests"
	@echo "  make install-validator   — install rdap-org/validator CLI (requires Node.js 18+)"
	@echo "  make demo                — run gordap in demo mode on :8080"
	@echo "  make demo-synth N=10000  — same, but seed N synthetic records"
	@echo "  make seed N=100000       — bulk-insert N synthetic rows into DATABASE_URL"
	@echo "  make stress C=100 D=30s  — run load + correctness validator against :8080"
	@echo "  make stress-aggregate    — combine N stress JSON reports (REPORTS=foo*.json)"
	@echo "  make build-tools         — build all gordap-* binaries to bin/"
	@echo "  make lint                — go vet"

build:
	go build -o bin/gordap ./cmd/gordap

build-tools: build
	go build -o bin/gordap-seed ./cmd/gordap-seed
	go build -o bin/gordap-stress ./cmd/gordap-stress
	go build -o bin/gordap-stress-aggregate ./cmd/gordap-stress-aggregate

test:
	go test -count=1 ./...

test-race:
	go test -count=1 -race ./...

test-realworld: build
	go test -count=1 -tags=realworld -v ./test/realworld/...

# Pulls the ICANN RDAP Conformance Tool image and runs it against a
# locally booted gordap. The image name below is illustrative — ICANN
# publishes the tool at https://github.com/icann/rdap-conformance-tool
# and operators typically vendor their own tag. The Docker invocation
# here assumes the tool is reachable as `icann/rdap-conformance-tool`.
test-conformance: build
	@echo "Starting gordap on :8080 in the background..."
	./bin/gordap -addr=:8080 -self-link-base=http://host.docker.internal:8080 -icann-gtld -tos-url=https://example/tos &
	@SERVER_PID=$$!; \
	  sleep 2; \
	  docker run --rm --add-host host.docker.internal:host-gateway \
	    icann/rdap-conformance-tool \
	    --profile icann-rdap-response-profile-2.2 \
	    http://host.docker.internal:8080/domain/example.nl; \
	  STATUS=$$?; \
	  kill $$SERVER_PID 2>/dev/null; \
	  exit $$STATUS

install-openrdap:
	go install github.com/openrdap/rdap/cmd/rdap@latest

# Clones rdap-org/validator.rdap.org into a cache dir, runs `npm i`,
# and symlinks the CLI into $GOBIN (or $HOME/go/bin). Requires Node
# 18+ and git. The validator is a pure JS tool; no native deps.
install-validator:
	@set -e; \
	CACHE=$${GORDAP_VALIDATOR_CACHE:-$$HOME/.cache/gordap/validator}; \
	BIN=$${GOBIN:-$$HOME/go/bin}; \
	mkdir -p $$CACHE $$BIN; \
	if [ ! -d $$CACHE/.git ]; then \
	  git clone --depth 1 https://github.com/rdap-org/validator.rdap.org $$CACHE; \
	else \
	  git -C $$CACHE pull --ff-only; \
	fi; \
	(cd $$CACHE && npm i --silent); \
	ln -sf $$CACHE/bin/rdap-validator $$BIN/rdap-validator; \
	echo "installed rdap-validator -> $$BIN/rdap-validator"

validate: build
	@docker run --rm --network host \
	  node:20-alpine sh -c "\
	    git clone --depth 1 https://github.com/rdap-org/validator.rdap.org /val && \
	    cd /val && npm i --silent && \
	    bin/rdap-validator http://host.docker.internal:8080/domain/example.nl"

demo: build
	./bin/gordap -addr=:8080 -self-link-base=http://localhost:8080 -icann-gtld -tos-url=https://example/tos

# N defaults to 10000; override with `make demo-synth N=100000`. Sets
# rate-limit-rps=0 so a stress run isn't capped by it; production
# deployments should leave the rate limiter on.
N ?= 10000
demo-synth: build
	./bin/gordap -addr=:8080 -self-link-base=http://localhost:8080 \
	  -demo-synth=$(N) -rate-limit-rps=0 \
	  -debug-addr=127.0.0.1:6060

# Postgres seeder. Requires DATABASE_URL.
seed: build-tools
	./bin/gordap-seed -n=$(N) -truncate

# Load + correctness validator. Override C / D / N as needed.
C ?= 100
D ?= 30s
URL ?= http://localhost:8080
stress: build-tools
	./bin/gordap-stress -url=$(URL) -c=$(C) -d=$(D) -n=$(N) -warmup=2s

# Roll up JSON reports from horizontally-scaled generators.
# Usage: make stress-aggregate REPORTS="run-host1.json run-host2.json"
REPORTS ?=
stress-aggregate: build-tools
	@if [ -z "$(REPORTS)" ]; then \
	  echo "usage: make stress-aggregate REPORTS=\"foo.json bar.json ...\""; \
	  exit 2; \
	fi; \
	./bin/gordap-stress-aggregate $(REPORTS)

lint:
	go vet ./...

clean:
	rm -rf bin/
