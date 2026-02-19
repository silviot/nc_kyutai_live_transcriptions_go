#!/usr/bin/env bash
set -e

# Build script for transcription service

echo "🔨 Building transcription service..."

# Go build
go build -o transcribe-service ./cmd/transcribe-service

echo "✅ Binary built: ./transcribe-service"
echo ""
echo "📖 Usage:"
echo "  ./transcribe-service -port 8080"
echo ""
echo "🌍 Environment variables:"
echo "  LT_HPB_URL=wss://hpb.example.com"
echo "  LT_INTERNAL_SECRET=your-secret"
echo "  STT_STREAM_URL=ws://100.66.230.65:8000/v1/stream   # or use Modal vars below"
echo "  MODAL_WORKSPACE=your-workspace"
echo "  MODAL_KEY=your-key"
echo "  MODAL_SECRET=your-secret"
