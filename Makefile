.PHONY: build test run sim deps

build: deps
	go build -o elm327-server ./cmd/elm327-server
	go build -o elm327-sim ./cmd/elm327-sim

test: web/ui/node_modules
	go test ./...

# a simulated car: http://localhost:8080
run: web/ui/node_modules
	go run ./cmd/elm327-server -simulate -allow-clear

# an ELM327 emulator on TCP port 35000, for other OBD apps
sim:
	go run ./cmd/elm327-sim

# Install the browser dependencies from the lockfile. go:embed bundles them into
# the binary, so run this before building; nothing else needs Node.
deps:
	npm ci --prefix web/ui --no-audit --no-fund

# Install only when missing or older than the lockfile, for test and run.
web/ui/node_modules: web/ui/package-lock.json
	npm ci --prefix web/ui --no-audit --no-fund
