FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

COPY example/backend/pyproject.toml example/backend/README.md example/backend/main.py ./
RUN pip install --no-cache-dir .

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1", "--ws-max-size", "65536", "--ws-max-queue", "16", "--ws-ping-interval", "30", "--ws-ping-timeout", "10"]
