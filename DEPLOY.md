# Deployment Guide

## Pre-Deployment Checklist

- [ ] Go 1.22+ installed (or Docker available)
- [ ] Modal workspace credentials configured
- [ ] Nextcloud Talk with HPB enabled
- [ ] TURN servers configured in Talk
- [ ] Network access to HPB and Modal endpoints

## Local Development

### Build

```bash
./scripts/build.sh
```

### Test

```bash
./scripts/test.sh
```

### Run

```bash
export LT_HPB_URL="wss://your-hpb-server"
export LT_INTERNAL_SECRET="your-secret"
export MODAL_WORKSPACE="your-workspace"
export MODAL_KEY="your-key"
export MODAL_SECRET="your-secret"

./transcribe-service -port 8080
```

Test health endpoint:
```bash
curl http://localhost:8080/healthz
```

## Docker Deployment

### Build Image

```bash
docker build -t kyutai-transcribe:latest .
```

### Run Container

```bash
docker run \
  -p 8080:8080 \
  -e LT_HPB_URL="wss://your-hpb-server" \
  -e LT_INTERNAL_SECRET="your-secret" \
  -e MODAL_WORKSPACE="your-workspace" \
  -e MODAL_KEY="your-key" \
  -e MODAL_SECRET="your-secret" \
  kyutai-transcribe:latest
```

### Docker Compose (Recommended)

```bash
# Create .env file
cat > .env << EOF
LT_HPB_URL=wss://your-hpb-server
LT_INTERNAL_SECRET=your-secret
MODAL_WORKSPACE=your-workspace
MODAL_KEY=your-key
MODAL_SECRET=your-secret
EOF

# Start service
docker-compose up -d

# View logs
docker-compose logs -f transcribe-service

# Stop service
docker-compose down
```

## Kubernetes Deployment

### Deployment Manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kyutai-transcribe
spec:
  replicas: 2
  selector:
    matchLabels:
      app: kyutai-transcribe
  template:
    metadata:
      labels:
        app: kyutai-transcribe
    spec:
      containers:
      - name: transcribe
        image: kyutai-transcribe:latest
        ports:
        - containerPort: 8080
        env:
        - name: LT_HPB_URL
          value: wss://hpb.example.com
        - name: LT_INTERNAL_SECRET
          valueFrom:
            secretKeyRef:
              name: transcribe-secrets
              key: hpb-secret
        - name: MODAL_WORKSPACE
          valueFrom:
            secretKeyRef:
              name: transcribe-secrets
              key: modal-workspace
        - name: MODAL_KEY
          valueFrom:
            secretKeyRef:
              name: transcribe-secrets
              key: modal-key
        - name: MODAL_SECRET
          valueFrom:
            secretKeyRef:
              name: transcribe-secrets
              key: modal-secret
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "2000m"
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10

---
apiVersion: v1
kind: Service
metadata:
  name: kyutai-transcribe-service
spec:
  selector:
    app: kyutai-transcribe
  ports:
  - protocol: TCP
    port: 80
    targetPort: 8080
  type: ClusterIP
```

### Create Secrets

```bash
kubectl create secret generic transcribe-secrets \
  --from-literal=hpb-secret="your-secret" \
  --from-literal=modal-workspace="your-workspace" \
  --from-literal=modal-key="your-key" \
  --from-literal=modal-secret="your-secret"
```

### Deploy

```bash
kubectl apply -f deployment.yaml
```

## Production Configuration

### Environment Variables

Required:
- `LT_HPB_URL` - HPB WebSocket URL (e.g., `wss://hpb.example.com`)
- `LT_INTERNAL_SECRET` - HPB HMAC secret
- `MODAL_WORKSPACE` - Modal workspace name
- `MODAL_KEY` - Modal API key
- `MODAL_SECRET` - Modal API secret

Optional:
- `PORT` - HTTP port (default: 8080)
- `LOG_LEVEL` - debug|info|warn|error (default: info)

### Resource Allocation

Per container (single instance):
- **Memory**: 512MB minimum, 2GB recommended
- **CPU**: 500m minimum, 1-2 cores recommended
- **Network**: 10 Mbps+ recommended
- **Concurrent speakers**: 20-50 per container (at 50MB each)

Scale horizontally by deploying multiple containers behind a load balancer.

### Health Checks

Health endpoint: `GET /healthz`
- Returns 200 OK if service is healthy
- Check every 30 seconds
- Timeout: 5 seconds

Metrics endpoint: `GET /metrics` (Prometheus format)

### Graceful Shutdown

Container receives SIGTERM signal. Service:
1. Stops accepting new transcriptions
2. Waits up to 5 seconds for in-flight requests
3. Closes all rooms and connections
4. Exits with code 0

Configure container `terminationGracePeriodSeconds` to allow shutdown.

## Monitoring

### Logs

All logs are JSON-formatted to stdout for easy parsing by container logging systems.

Log levels:
- `debug` - Development only (verbose)
- `info` - Normal operation
- `warn` - Recoverable issues
- `error` - Failures

### Metrics

Prometheus metrics available at `GET /metrics`:
- `transcription_rooms_active` - Number of active rooms
- `transcription_speakers_active` - Number of active speakers

### Alerting

Suggested alerts:
- Health check failing (service down)
- High memory usage (>80% of limit)
- Increasing error rates
- Modal connection failures

## Rollback Plan

If issues occur:

1. **Immediate**: Revert DNS/load balancer to point to Python version
2. **Short-term**: Keep Python service running alongside Go service
3. **Investigation**: Check logs for errors in `pkg/session`, `pkg/hpb`, `pkg/modal`
4. **Fix**: Deploy patched version
5. **Gradual rollout**: Test with 10% traffic before full cutover

## Performance Validation

After deployment, verify:

1. **Memory usage**: Should be <50 MB per speaker (check via `docker stats` or pod metrics)
2. **Connection stability**: Monitor HPB and Modal connection logs
3. **Latency**: Measure time from audio capture to first transcript token (target: <1s)
4. **Throughput**: Monitor concurrent speaker count (target: 500+ per instance)
5. **Error rates**: Check `/metrics` for error counters

## Troubleshooting

### Service won't start
- Check all environment variables are set
- Verify HPB URL and credentials
- Check network connectivity to Modal workspace

### High memory usage
- Reduce concurrent speakers per container (scale horizontally)
- Check for goroutine leaks in logs
- Monitor audio frame buffering

### Lost transcriptions
- Enable persistent logging for transcription pipeline
- Check Modal connection logs for disconnects
- Verify HPB message delivery (may be lost if Room service crashes)

### Slow latency
- Measure cold start penalty (first request to Modal ~120s)
- Check network latency to Modal endpoint
- Monitor resampling performance (may benefit from libsoxr optimization)

## Support

For issues:
1. Check logs: `docker logs <container-id>`
2. Check metrics: `curl http://localhost:8080/metrics`
3. Test health: `curl http://localhost:8080/healthz`
4. Review GitHub issues: https://github.com/silviot/nc_kyutai_live_transcriptions_go
