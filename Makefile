.PHONY: help build test test-race test-realworld test-conformance install-openrdap install-validator validate lint clean run demo

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
	@echo "  make lint                — go vet"

build:
	go build -o bin/gordap ./cmd/gordap

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

lint:
	go vet ./...

clean:
	rm -rf bin/
