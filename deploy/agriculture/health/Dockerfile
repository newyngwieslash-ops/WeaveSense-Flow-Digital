FROM python:3.12.10-slim-bookworm
RUN pip install --no-cache-dir asyncpg==0.30.0 \
    && useradd --uid 10001 --create-home app
WORKDIR /app
COPY health.py .
USER 10001:10001
EXPOSE 8000
CMD ["python", "health.py"]
