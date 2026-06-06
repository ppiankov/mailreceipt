VERSION_NUM ?= 0.1.0
LDFLAGS := -X github.com/obstalabs/mailreceipt/internal/cli.version=$(VERSION_NUM)

.PHONY: build test vet fmt lint clean demo

build:
	go build -ldflags "$(LDFLAGS)" -o mailreceipt ./cmd/mailreceipt

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint: vet
	gofmt -l .

clean:
	rm -f mailreceipt
	rm -rf dist/

demo: build
	./mailreceipt check testdata/reminder-1509.eml --log testdata/mail.log --case DEMO-1
