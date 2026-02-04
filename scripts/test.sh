#!/usr/bin/env bash
set -e

# Test script for transcription service

echo "🧪 Running tests..."
echo ""

# Run unit tests
echo "📋 Unit tests:"
go test -v ./pkg/audio
go test -v ./pkg/hpb
echo "✅ Core unit tests passed"
echo ""

# Run all tests with coverage
echo "📊 Coverage:"
go test -cover ./...
echo ""

echo "✅ All tests passed!"
echo ""
echo "📝 To run integration tests (requires live servers):"
echo "  go test -v ./test"
