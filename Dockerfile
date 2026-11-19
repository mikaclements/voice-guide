# The images compose's app profile runs (scripts/up.sh), all built from the repository's root, where
# the lockfiles are: voice, whose image also runs the one-time setup; the model service; and the test
# page. Bases are pinned by digest, as compose's images are, so every checkout builds the same.

# The JavaScript packages from the lockfile, every workspace's: the drop-in client's build and the
# page take them all.
FROM node:26-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS packages
WORKDIR /app
COPY package.json package-lock.json ./
COPY schematic/package.json schematic/
COPY services/model/package.json services/model/
COPY web/package.json web/
RUN npm ci

FROM packages AS client
COPY web web
RUN npm run build:client --workspace web

# Pure Go, so the binaries need no C library in the image.
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY migrations migrations
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/voice ./cmd/migrate ./cmd/publish

# voice, and the setup before it: psql seeds the database as scripts/lib/seed.sh does.
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS voice
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates postgresql-client \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go /out/ /usr/local/bin/
COPY --from=client /app/web/dist-client web/dist-client
COPY agents agents
COPY scripts/lib/container.sh scripts/lib/seed.sql scripts/lib/
USER nobody
ENTRYPOINT ["scripts/lib/container.sh"]
CMD ["voice"]

# The model service. Claude Code's login lives in the Mac's keychain, which no container reaches, so
# it answers with the API or the stand-in replies only.
FROM node:26-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS model
WORKDIR /app
COPY package.json package-lock.json ./
COPY schematic/package.json schematic/
COPY services/model/package.json services/model/
COPY web/package.json web/
RUN npm ci --omit=dev --workspace services/model
COPY services/model/src services/model/src
USER node
ENTRYPOINT ["sh", "-c", "if [ \"$MODEL_PROVIDER\" = claude-subscription ]; then echo 'MODEL_PROVIDER=claude-subscription uses Claude Code signed in on this Mac, which a container cannot reach: use claude-api or fake here, or scripts/run.sh' >&2; exit 1; fi; exec node services/model/src/main.ts"]

# The test page, served by its Vite dev server, which plays the product's server (web/dev/product.ts).
# Vite writes beside its config and its cache under web/, so the page's user owns the folder.
FROM packages AS page
COPY web web
RUN chown -R node:node web
USER node
CMD ["npm", "run", "--silent", "dev", "--workspace", "web", "--", "--host", "0.0.0.0"]
