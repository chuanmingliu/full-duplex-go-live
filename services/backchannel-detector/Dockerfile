FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY detector.py server.py model.lock.json ./
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOST=0.0.0.0 PORT=18087 MODEL_DIR=/models
USER 65532:65532
CMD ["python", "server.py"]
