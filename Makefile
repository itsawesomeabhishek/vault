.PHONY: test lint vet fmt engine app

engine:
	cd engine && go test ./... -count=1 -timeout 10m

vet:
	cd engine && gofmt -l . && go vet ./...

app:
	cd app && npm run typecheck && npm test && npm run lint

test: engine app

lint: vet
	cd app && npm run lint
