BINARY := sslug
GO     ?= go

.PHONY: build test vet fmt validate run mock e2e clean

build:
	$(GO) build -o $(BINARY) ./cmd/sslug

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

validate: fmt-check vet test secret-scan

fmt-check:
	@out=$$($(GO) fmt ./...); if [ -n "$$out" ]; then echo "files reformatted: $$out"; exit 1; fi

secret-scan:
	@if git rev-parse --git-dir >/dev/null 2>&1; then \
		found=$$(git grep -nE '(sk-[A-Za-z0-9]{20}|BEGIN (RSA |EC )?PRIVATE KEY)' -- . 2>/dev/null); \
		if [ -n "$$found" ]; then echo "SECRET LEAK:"; echo "$$found"; exit 1; fi; \
	fi

run: build
	./$(BINARY)

mock:
	$(GO) run ./tools/mockprovider

e2e: build
	$(GO) build -o mockprovider ./tools/mockprovider
	bash tools/e2e/check.sh

clean:
	rm -f $(BINARY) cover.out
