VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/nbullorini/laranest/internal/version.Version=$(VERSION)
BIN := bin/laranest

.PHONY: build ui go windows dev test clean install publish

# Until the repo is public and stable every release is v0.1.0, re-published
# from the current main commit (the Release workflow replaces it).
RELEASE ?= v0.1.0

build: ui go

ui:
	cd web && npm ci && npm run build

go:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/laranest

# Windows: the sign-in launcher (a GUI program, so it opens no console) is
# embedded into laranest.exe, which installs it.
windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o internal/service/laranestw.exe ./cmd/laranestw
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/laranest.exe ./cmd/laranest

# Run daemon (API on :9150) + Vite dev server (UI with HMR on :5173).
dev:
	go run ./cmd/laranest serve & cd web && npm run dev

test:
	go test ./...

install: build
	install -Dm755 $(BIN) $(HOME)/.local/bin/laranest
	$(HOME)/.local/bin/laranest install

publish:
	@test -z "$$(git status --porcelain)" || { echo "uncommitted changes"; exit 1; }
	git push origin HEAD:main
	git tag -f -a $(RELEASE) -m "Laranest $(RELEASE)"
	git push -f origin $(RELEASE)

clean:
	rm -rf bin web/dist/assets web/dist/index.html internal/service/laranestw.exe
