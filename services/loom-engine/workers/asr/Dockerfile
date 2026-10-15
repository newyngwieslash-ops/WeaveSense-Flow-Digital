# ASR worker (compose profile "asr"): faster-whisper on CPU (int8). The model is
# downloaded at build time (ASR_MODEL, default "small") so the container needs no egress.
FROM python:3.12-slim
ARG ASR_MODEL=small
ENV ASR_MODEL=${ASR_MODEL} ASR_MODEL_DIR=/models
WORKDIR /app
COPY requirements.txt requirements-model.txt ./
RUN pip install --no-cache-dir -r requirements.txt -r requirements-model.txt \
    && python -c "from faster_whisper import WhisperModel; WhisperModel('${ASR_MODEL}', device='cpu', compute_type='int8', download_root='/models')" \
    && chmod -R a+rX /models
COPY server.py .
USER 65534:65534
EXPOSE 8797
HEALTHCHECK --interval=30s --timeout=5s CMD python -c "import urllib.request;urllib.request.urlopen('http://127.0.0.1:8797/healthz')"
CMD ["python", "server.py"]
