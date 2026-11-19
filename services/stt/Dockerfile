# The speech-to-text service with one size of Moonshine's models, fetched at build and checked
# against models.sha256. Images are pinned by digest, so every build starts from the same files;
# Python's version comes from .python-version, and uv refuses to run unless it is the version
# pyproject.toml requires.
FROM ghcr.io/astral-sh/uv:0.13.7@sha256:96f40d3904aad8d0bd010539001d36c8e2188b981e1334471a90c4a1d93bb490 AS uv

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS build
# For fetching the models over HTTPS; this stage is thrown away.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=uv /uv /usr/local/bin/uv
ENV UV_PYTHON_INSTALL_DIR=/python UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1
WORKDIR /app
COPY .python-version pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev
ARG STT_MODEL=small
COPY models.sha256 ./
COPY stt/models.py stt/
RUN .venv/bin/python stt/models.py /models "$STT_MODEL"
COPY stt stt

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
# The service runs the size it was built with.
ARG STT_MODEL=small
ENV STT_MODEL=$STT_MODEL
COPY --from=build /python /python
COPY --from=build /models /models
COPY --from=build /app /app
WORKDIR /app
ENV PATH=/app/.venv/bin:$PATH STT_HOST=0.0.0.0 STT_MODELS=/models
USER nobody
EXPOSE 8092
CMD ["python", "-m", "stt"]
