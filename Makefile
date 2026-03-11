TOKEN=${GITHUB_TOKEN}
REF=main
REPO=starkware-libs/merge-gatekeeper
IGNORED=""

go-build:
	GO111MODULE=on LANG=en_US.UTF-8 CGO_ENABLED=0 go build ./cmd/merge-gatekeeper

go-run: go-build
	./merge-gatekeeper validate --token=$(TOKEN) --ref $(REF) --repo $(REPO) --ignored "$(IGNORED)"

docker-build:
	docker build -t merge-gatekeeper:latest .

docker-run: docker-build
	docker run --rm -it --name merge-gatekeeper merge-gatekeeper:latest validate --token=$(TOKEN) --ref $(REF) --repo $(REPO) --ignored "$(IGNORED)"

test:
	go test ./...