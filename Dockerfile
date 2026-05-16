FROM python:3.11-slim

WORKDIR /app

RUN pip install --no-cache-dir \
    "fastapi>=0.111" \
    "uvicorn[standard]>=0.29" \
    "fastembed==0.3.6" \
    "numpy>=1.26" \
    "apscheduler>=3.10" \
    "PyGithub>=2.3" \
    "python-ulid>=1.0" \
    "pyrage>=0.1" \
    "sqlite-vec==0.1.7" \
    "sqlalchemy[asyncio]>=2.0" \
    "asyncpg>=0.29" \
    "alembic>=1.13" \
    "pyyaml>=6" \
    "pytest>=8" \
    "pytest-asyncio>=0.23" \
    "httpx>=0.27"

# Bake bge-small ONNX into image (~80MB) and verify sqlite-vec wheel
RUN python -c "\
from fastembed import TextEmbedding; \
list(TextEmbedding('BAAI/bge-small-en-v1.5').embed(['warmup']))"
RUN python -c "import sqlite_vec; print('sqlite-vec', sqlite_vec.__version__)"

COPY . .

RUN pip install --no-cache-dir --no-deps .

EXPOSE 8765

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8765"]
