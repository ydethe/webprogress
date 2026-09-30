# Image that runs the webprogress server (NiceGUI app on port 8775).
# The client is a library used elsewhere; it is not run from this image.
FROM python:3.12-slim

# uv provides fast, reproducible installs from uv.lock.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

ENV PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy

WORKDIR /app

# The package version is derived from git tags via SCM, so .git must be present
# at build time for the install to succeed.
COPY dist/*.whl .

# Install only runtime dependencies (no dev/test/doc groups) into /app/.venv.
RUN uv pip install --system ./*.whl

# NiceGUI serves on 8775 (see server.run()).
EXPOSE 8775

# OIDC and session configuration is supplied at runtime via WEBPROGRESS_* env vars.
CMD ["uv", "run", "python", "-c", "from webprogress.server import run; run()"]
