FROM python:3.11-slim

WORKDIR /app

RUN pip install --no-cache-dir \
    "fastapi>=0.111" \
    "uvicorn[standard]>=0.29" \
    "fastembed>=0.3" \
    "numpy>=1.26" \
    "apscheduler>=3.10" \
    "PyGithub>=2.3" \
    "python-ulid>=1.0" \
    "pyrage>=0.1" \
    "pytest>=8" \
    "pytest-asyncio>=0.23" \
    "httpx>=0.27"

# Bake fastembed model into image to avoid cold-start download
RUN python -c "\
from fastembed import TextEmbedding; \
list(TextEmbedding('sentence-transformers/all-MiniLM-L6-v2').embed(['warmup']))"

COPY . .

# Register the project in site-packages metadata so importlib.metadata
# resolves __version__ at runtime. --no-deps because all runtime deps
# are already installed in the earlier layer (preserves cache reuse).
RUN pip install --no-cache-dir --no-deps .

EXPOSE 8765

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8765"]
