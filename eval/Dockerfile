# Both phases use the same Go toolchain. Only the smoke target receives private fixtures.
ARG GO_VERSION=1.25.0
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /build/strap-eval ./cmd/strap-eval \
    && CGO_ENABLED=0 go test -c -o /build/eval.test ./eval
RUN GOBIN=/build go install golang.org/x/tools/gopls@v0.20.0
RUN mkdir /public && for tier in easy medium hard; do \
      for task in eval/ladder/$tier/*; do \
        dest=/public/$tier/$(basename "$task"); \
        mkdir -p "$dest"; \
        cp "$task/task.json" "$dest/"; \
        cp -R "$task/workspace" "$dest/"; \
      done; \
    done

FROM golang:${GO_VERSION}-bookworm AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ripgrep poppler-utils \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /build/strap-eval /build/gopls /usr/local/bin/
COPY --from=build /public /problems
RUN mkdir /workspace /results /outbox
WORKDIR /workspace
ENTRYPOINT ["strap-eval"]

# A deterministic end-to-end check at the actual container paths, with no model server.
FROM runtime AS smoke
COPY --from=build /src/eval/ladder /grading
COPY --from=build /build/eval.test /usr/local/bin/eval.test
RUN STRAP_EVAL_CONTAINER_SMOKE=1 eval.test -test.run '^TestContainerWorkflow$' -test.v

FROM runtime AS final
