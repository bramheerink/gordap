.PHONY: help build test test-race test-realworld test-conformance install-openrdap lint clean run demo

help:
	@echo "gordap targets:"
	@echo "  make build              — build ./cmd/gordap"
	@echo "  make test               — unit + e2e tests"
	@echo "  make test-race          — tests under the race detector"
	@echo "  make test-realworld     — boot gordap + assert RFC compliance"
	@echo "  make test-conformance   — run ICANN RDAP Conformance Tool (Docker)"
	@echo "  make install-openrdap   — install openrdap/rdap CLI for interop tests"
	@echo "  make demo               — run gordap in demo mode on :8080"
	@echo "  make lint               — go vet"

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

demo: build
	./bin/gordap -addr=:8080 -self-link-base=http://localhost:8080 -icann-gtld -tos-url=https://example/tos

lint:
	go vet ./...

clean:
	rm -rf bin/
